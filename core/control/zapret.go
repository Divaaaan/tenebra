package control

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Divaaaan/tenebra/core/zapret"
)

// zapretDirName is where an imported bundle lives, beside the profile store.
const zapretDirName = "zapret"

// handleImportZapret installs a zapret bundle sent by the UI and reports the
// strategies it contains.
//
// The archive arrives as bytes rather than a path on purpose: the UI runs in a
// webview where a dropped file has contents but no filesystem path, and routing
// it through a temp file the UI writes would need filesystem permissions the
// renderer should not have. The daemon already holds the privileged side of
// this app; unpacking here keeps that boundary intact.
func (d *Daemon) handleImportZapret(req Request) Response {
	dir := filepath.Join(d.store.Dir(), zapretDirName)

	// A path is accepted too: Tauri's drag-drop hands the UI real filesystem
	// paths, which is the only way a dropped FOLDER can be taken at all — a
	// webview File carries bytes, and a folder has none. Someone who already
	// unpacked the release should not have to re-zip it.
	if req.Path != "" {
		strategies, err := zapret.Install(req.Path, dir)
		if err != nil {
			return newError(req.ID, err.Error())
		}
		d.recordImportedZapret(dir, req.Path)
		return zapretBundleResponse(req.ID, dir, strategies)
	}

	if req.Data == "" {
		return newError(req.ID, "import_zapret: пустой архив")
	}
	raw, err := base64.StdEncoding.DecodeString(req.Data)
	if err != nil {
		return newError(req.ID, fmt.Sprintf("import_zapret: битые данные: %v", err))
	}

	// zip.OpenReader needs a file, and the bundle is a few megabytes — small
	// enough to stage in temp and delete straight after.
	tmp, err := os.CreateTemp("", "tenebra-zapret-*.zip")
	if err != nil {
		return newError(req.ID, fmt.Sprintf("import_zapret: %v", err))
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = os.Remove(tmpPath)
	}()
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return newError(req.ID, fmt.Sprintf("import_zapret: %v", err))
	}
	if err := tmp.Close(); err != nil {
		return newError(req.ID, fmt.Sprintf("import_zapret: %v", err))
	}

	strategies, err := zapret.Install(tmpPath, dir)
	if err != nil {
		return newError(req.ID, err.Error())
	}
	// The temp file's name carries no version; the name the user dropped does.
	d.recordImportedZapret(dir, req.Name)
	return zapretBundleResponse(req.ID, dir, strategies)
}

// recordImportedZapret stamps an imported bundle with the version read from the
// file or folder name it came from, and refreshes the reported state.
//
// Without the stamp the first update check sees "unknown" and re-downloads the
// release the user just installed by hand — harmless but daft. The release
// archives are named after their version, so the name is a real answer; a name
// that carries none leaves the version empty, which the updater treats as "older
// than anything published" and corrects on its next run.
func (d *Daemon) recordImportedZapret(dir, source string) {
	if v := zapret.VersionFromName(source); v != "" {
		if err := zapret.WriteVersion(dir, v); err != nil {
			d.emitLog(LogWarn, fmt.Sprintf("zapret: не записать версию сборки: %v", err))
		}
	}
	d.mu.Lock()
	d.refreshZapretStateLocked()
	d.mu.Unlock()
}

// zapretBundleResponse renders the installed-bundle reply shared by the archive
// and folder paths, so the two cannot drift apart in what they report.
func zapretBundleResponse(id int64, dir string, strategies []zapret.Strategy) Response {
	names := make([]string, len(strategies))
	for i, s := range strategies {
		names[i] = s.Name
	}
	resp, err := newResult(id, struct {
		Dir        string   `json:"dir"`
		Strategies []string `json:"strategies"`
	}{Dir: dir, Strategies: names})
	if err != nil {
		return newError(id, err.Error())
	}
	return resp
}

// refreshZapretStateLocked mirrors the bypass's state into the reported status.
// Callers must hold d.mu.
//
// The version is read from disk here rather than cached in a field because the
// bundle directory is the only place it can be authoritative: an update, an
// import, or a user replacing the folder by hand all change it, and a cached
// copy would keep reporting a version that is no longer installed. Status is not
// hot enough for one small file read to matter.
func (d *Daemon) refreshZapretStateLocked() {
	d.state.ZapretActive = d.routing.ZapretActive
	d.state.ZapretStrategy = d.zapretActive
	d.state.ZapretAutoUpdate = d.zapretAutoUpdate
	d.state.ZapretVersion = zapret.Version(filepath.Join(d.store.Dir(), zapretDirName))
}

// newZapretRunner builds a bypass runner that knows whether a tunnel is up.
//
// The one thing it decides is the bundle's real-time-UDP block (see
// zapret.StripVoiceUDP). With no tunnel that block is what makes Discord's voice
// work: it punches the UDP through the DPI on the direct path. With a tunnel up
// it does the opposite — WinDivert takes those packets before the tunnel's
// router ever sees them and reinjects them direct, onto a path where the voice
// servers answer nothing, so the client reports "no route" while the routing
// table says the tunnel should have carried it. Measured both ways on the
// author's network: bypass running, voice UDP leaves from the ISP address and
// never connects; bypass stopped, it leaves through the node and works — while
// YouTube dies with it. Dropping just that block is what lets both work at once.
func (d *Daemon) newZapretRunner(dir string) *zapret.Runner {
	return d.newZapretRunnerFor(dir, d.snapshotState().State == StateConnected)
}

// newZapretRunnerFor is newZapretRunner with the tunnel question answered by the
// caller. The connect path has to answer it itself: it raises the bypass before
// the tunnel exists, so reading the state there always says "not connected" and
// leaves the voice block in — on a session that is about to have a tunnel. The
// symptom is a machine that connects and then cannot hold a voice call, with
// nothing in the logs to explain it, because the block was decided one step too
// early.
func (d *Daemon) newZapretRunnerFor(dir string, tunnelUp bool) *zapret.Runner {
	r := zapret.NewRunner(dir)
	r.KeepVoiceInTunnel = tunnelUp
	// Keep the filter off our own tunnel's adapter. Pinned always, not only while
	// connected: a strategy started before the tunnel is still running after it
	// comes up, and by then the damage is done invisibly.
	r.PinIfaceIndex = d.physicalIfaceIndex()
	return r
}

// excludeNodesFromZapret keeps the packet filter off the tunnel's own
// connections by listing every stored node address in the bundle's exclusion
// list, before a strategy is launched (winws reads the lists at startup).
//
// Every profile's nodes are listed, not just the connected one: the fallback
// walk moves between nodes on its own, and an exclusion that only covered the
// current exit would stop protecting the moment it failed over — precisely when
// the tunnel can least afford a second, self-inflicted fault.
//
// Best-effort: a bundle we cannot write to is a reason to log, not to leave the
// user without a bypass.
func (d *Daemon) excludeNodesFromZapret(dir string) {
	var servers []string
	for _, p := range d.store.List() {
		for _, n := range p.Servers {
			servers = append(servers, n.Server)
		}
	}
	unresolved, err := d.zapretExclude(dir, servers)
	if err != nil {
		d.emitLog(LogWarn, fmt.Sprintf("zapret: не удалось исключить адреса узлов из фильтра: %v", err))
	}
	// A node addressed by name whose address we could not learn stays in the
	// filter's way, and the symptom is the one this whole function exists to
	// prevent: a port that opens and goes quiet, read as a dead node. Name the
	// nodes, so the failure can be traced to DNS instead of to the subscription.
	if len(unresolved) > 0 {
		d.emitLog(LogWarn, fmt.Sprintf(
			"zapret: не удалось определить адреса узлов (%d): %s — фильтр может рвать подключение к ним",
			len(unresolved), strings.Join(shortNameList(unresolved), ", ")))
	}
}

// logNameLimit caps how many node names one warning spells out: a subscription
// with a hundred dead names would otherwise push the rest of the log out of the
// buffer with one line.
const logNameLimit = 5

// shortNameList trims a name list to something a log line can carry, keeping the
// count of what it dropped.
func shortNameList(names []string) []string {
	if len(names) <= logNameLimit {
		return names
	}
	out := append([]string(nil), names[:logNameLimit]...)
	return append(out, fmt.Sprintf("и ещё %d", len(names)-logNameLimit))
}

// applyZapretState records that the bypass came up (or went down) and makes the
// routing follow it.
//
// The two must move together. Routing sends YouTube and Discord down the direct
// path *because* the bypass is carrying them there; leaving the flag set after
// the bypass stops points those services at a censor with nothing to get them
// through, so the user sees a connected VPN and dead video — worse than either
// state alone. The reverse is milder but still wrong: a bypass running while
// routing believes it is not tunnels traffic that no longer needs the detour.
//
// running strategies are re-read from the bundle every time it comes up, because
// the user can re-import a different bundle between runs and the coverage decides
// which services are safe to leave on the direct path.
//
// A live tunnel is rebuilt in place (reapplyLive) rather than waiting for a
// reconnect: the config carries the split, so without the rebuild the switch the
// user just pressed would do nothing until they disconnected and connected again.
func (d *Daemon) applyZapretState(running bool, strategy string) {
	dir := filepath.Join(d.store.Dir(), zapretDirName)

	var covered []string
	if running {
		covered = zapret.Covered(dir)
	}

	d.mu.Lock()
	if strategy != "" {
		d.zapretActive = strategy
	}
	changed := d.routing.ZapretActive != running
	d.routing.ZapretActive = running
	d.routing.ZapretCovered = covered
	d.routing = d.routing.Normalize()
	d.refreshZapretStateLocked()
	d.mu.Unlock()

	d.persistSettings()

	if changed {
		d.reapplyLive()
	}
}

// raiseZapretForConnect brings the bypass up as part of connecting and records
// where that leaves the routing.
//
// Shared by the user-driven connect and autoconnect so the two cannot drift:
// an autoconnected session that skipped this would tunnel YouTube and Discord at
// full round-trip latency, or — if an earlier run left the routing expecting the
// bypass — leave them uncarried entirely, which is the same app behaving like two
// different products depending on whether a button was pressed.
//
// It does not rebuild a live tunnel: both callers are about to build one, and the
// flag set here is what that build reads.
func (d *Daemon) raiseZapretForConnect(ctx context.Context) bool {
	d.pickFreeTunAddress()
	d.installZapretIfMissing(ctx)
	// The tunnel is not up yet — this runs on the way to it — but it is about to
	// be, and the voice block has to be decided for the session that will exist.
	up := d.autoStartZapret(ctx, true)
	d.mu.Lock()
	d.routing.ZapretActive = up
	if !up {
		// Stale coverage from an earlier run would keep routing services direct
		// with nothing carrying them there.
		d.routing.ZapretCovered = nil
	}
	d.refreshZapretStateLocked()
	strategy := d.zapretActive
	covered := len(d.routing.ZapretCovered)
	d.mu.Unlock()
	// One line that settles where the censored services will actually go on this
	// connect. It is the answer to the most common report the app gets — "the VPN
	// is on but YouTube is still slow" — and until now it had to be inferred from
	// two or three other lines that may not both be present.
	if up {
		d.emitLog(LogInfo, fmt.Sprintf("zapret: обход поднят на стратегии %s, %d сервис(ов) идут напрямую", strategy, covered))
	} else {
		d.emitLog(LogInfo, "zapret: обход не поднят — YouTube и Discord пойдут через туннель")
	}
	return up
}

// zapretInstallBudget bounds the first-run bundle download so a connect cannot
// hang on it. The published archive is a couple of megabytes and installs in
// about a second and a half on a working link; a minute is far past that and far
// short of "the button did nothing".
const zapretInstallBudget = 60 * time.Second

// installZapretIfMissing fetches the bypass bundle when the user has none, so a
// first connect does not need one dragged in by hand.
//
// This is the difference between the product as described — paste a link, press
// one button — and one that first asks the user to find a release page, pick the
// right asset and unpack it. The bundle is not optional equipment: without it
// YouTube and Discord go through the tunnel at full round-trip latency, which is
// the thing the app exists to avoid.
//
// Bounded and best-effort by construction. A failure here is not a failed
// connect: the tunnel alone still carries everything, just slower, and the log
// says why. Held to the user's auto-update choice as well — someone who turned
// updates off did not ask this app to fetch anything.
func (d *Daemon) installZapretIfMissing(ctx context.Context) {
	dir := filepath.Join(d.store.Dir(), zapretDirName)
	if len(zapret.Discover(dir, dirFileNames(dir))) > 0 {
		return
	}
	d.mu.Lock()
	allowed := d.zapretAutoUpdate
	d.mu.Unlock()
	if !allowed {
		return
	}

	d.emitLog(LogInfo, "zapret: сборки нет — ставлю сам")
	ctx, cancel := context.WithTimeout(ctx, zapretInstallBudget)
	defer cancel()
	if _, to, ok, err := d.updateZapret(ctx); err != nil {
		switch {
		case errors.Is(err, zapret.ErrIntegrity):
			// A bundle that failed verification is not "could not download": the
			// archive arrived and was not what upstream published. Everything else on
			// this path is a connectivity problem the user can ignore; this one is not.
			d.reportZapretUpdateFailure(err)
		case errors.Is(err, zapret.ErrUntrustedVersion):
			// Upstream has moved past every pin this build carries, so there is no
			// bundle it can install on trust. Nothing is fetched; the tunnel carries
			// the censored services on its own until a Tenebra update brings the pin.
			d.emitLog(LogInfo, fmt.Sprintf(
				"zapret: доступна сборка обхода %s, но она новее вшитых в Tenebra проверок — "+
					"обнови Tenebra; туннель пока работает без обхода", versionLabel(to)))
		default:
			d.emitLog(LogWarn, fmt.Sprintf(
				"zapret: не удалось поставить сборку (%v) — туннель работает без обхода", err))
		}
	} else if ok {
		d.emitLog(LogInfo, fmt.Sprintf("zapret: поставлена сборка %s", to))
	}
}

// dirFileNames lists the file names in dir, or nothing when it cannot be read —
// which Discover reads as "no strategies", the same answer an empty directory
// gives.
func dirFileNames(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names
}

// handlePickZapret probes every installed strategy and reports which one to use.
//
// It runs synchronously and takes minutes: each strategy needs the packet filter
// attached, five control requests, and a clean detach before the next. That is
// the honest cost of the answer — the alternative is asking the user to try
// twenty batch files by hand.
func (d *Daemon) handlePickZapret(ctx context.Context, req Request) Response {
	// One bypass at a time: a probe run stops and starts winws twenty times and
	// must not race an update replacing the very files it is launching.
	d.zapretOpMu.Lock()
	defer d.zapretOpMu.Unlock()

	dir := filepath.Join(d.store.Dir(), zapretDirName)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return newError(req.ID, "pick_zapret: сначала загрузи сборку zapret")
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	strategies := zapret.Discover(dir, names)
	if len(strategies) == 0 {
		return newError(req.ID, "pick_zapret: в сборке нет стратегий")
	}

	// Every strategy in the run attaches its own filter, so the exclusion has to be
	// in place before the first one — a probe run that keeps knocking over the live
	// tunnel would also poison its own measurements.
	d.excludeNodesFromZapret(dir)

	runner := d.newZapretRunner(dir)
	results, baseline, err := runner.Pick(ctx, strategies, zapret.DefaultTargets(), func(r zapret.Result) {
		// Report as it goes: a silent multi-minute operation reads as a hang,
		// and the user should see which strategy is being tried.
		d.emitLog(LogInfo, fmt.Sprintf("zapret: %s — %d/%d", r.Name, r.OKCount(), len(r.Targets)))
	})
	if err != nil {
		return newError(req.ID, err.Error())
	}

	best, found := zapret.Best(results, baseline)
	ranked := zapret.Rank(results)

	// Leave the winner RUNNING. Probing ends with everything stopped so the
	// machine is not left on whichever strategy happened to be last — but
	// stopping the winner too would mean the user watched a five-minute
	// measurement and got nothing switched on, which is how this first shipped
	// and exactly what was reported: "the bypass itself does not work".
	if found {
		if started, sErr := runner.Start(ctx, best.Strategy); sErr != nil || !started {
			d.emitLog(LogWarn, fmt.Sprintf("zapret: %s не запустилась после подбора", best.Name))
		} else {
			d.applyZapretState(true, best.Name)
			d.emitLog(LogInfo, fmt.Sprintf("zapret: включена %s", best.Name))
		}
	}

	out := struct {
		Baseline int             `json:"baseline"`
		Targets  int             `json:"targets"`
		Best     string          `json:"best,omitempty"`
		Improved bool            `json:"improved"`
		Results  []zapret.Result `json:"results"`
	}{
		Baseline: baseline,
		Targets:  len(zapret.DefaultTargets()),
		Improved: found,
		Results:  ranked,
	}
	if found {
		out.Best = best.Name
	}

	resp, err := newResult(req.ID, out)
	if err != nil {
		return newError(req.ID, err.Error())
	}
	return resp
}

// handleListZapret reports the currently installed bundle, if any.
//
// A missing bundle is not an error: "nothing imported yet" is the normal state
// on first run, and returning a failure for it would make the UI show a problem
// where there is none.
func (d *Daemon) handleListZapret(req Request) Response {
	dir := filepath.Join(d.store.Dir(), zapretDirName)

	entries, err := os.ReadDir(dir)
	if err != nil {
		resp, mErr := newResult(req.ID, struct {
			Dir        string   `json:"dir"`
			Strategies []string `json:"strategies"`
		}{Dir: dir, Strategies: nil})
		if mErr != nil {
			return newError(req.ID, mErr.Error())
		}
		return resp
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	strategies := zapret.Discover(dir, names)
	out := make([]string, len(strategies))
	for i, s := range strategies {
		out[i] = s.Name
	}

	resp, err := newResult(req.ID, struct {
		Dir        string   `json:"dir"`
		Strategies []string `json:"strategies"`
	}{Dir: dir, Strategies: out})
	if err != nil {
		return newError(req.ID, err.Error())
	}
	return resp
}

// handleStartZapret turns the bypass on: the named strategy, or the one the
// last probe picked.
//
// This is the switch the user actually asked for. Probing answers "which one",
// but the answer is worthless if nothing is running afterwards.
func (d *Daemon) handleStartZapret(ctx context.Context, req Request) Response {
	d.zapretOpMu.Lock()
	defer d.zapretOpMu.Unlock()

	dir := filepath.Join(d.store.Dir(), zapretDirName)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return newError(req.ID, "start_zapret: сначала загрузи сборку zapret")
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	strategies := zapret.Discover(dir, names)
	if len(strategies) == 0 {
		return newError(req.ID, "start_zapret: в сборке нет стратегий")
	}

	want := req.Name
	if want == "" {
		d.mu.Lock()
		want = d.zapretActive
		d.mu.Unlock()
	}

	chosen := strategies[0] // the bundle's default, when nothing was picked yet
	if want != "" {
		found := false
		for _, s := range strategies {
			if s.Name == want {
				chosen, found = s, true
				break
			}
		}
		if !found {
			return newError(req.ID, fmt.Sprintf("start_zapret: стратегии %q нет в сборке", want))
		}
	}

	d.excludeNodesFromZapret(dir)

	// A hand-started bypass answers the tunnel question from the live state:
	// nothing is about to change here, unlike the connect path.
	runner := d.newZapretRunner(dir)
	started, err := runner.Start(ctx, chosen)
	if err != nil {
		return newError(req.ID, err.Error())
	}
	if !started {
		// winws not coming up is usually elevation: WinDivert needs a driver
		// load, which a non-elevated process cannot do. Saying that beats a bare
		// "failed".
		return newError(req.ID, fmt.Sprintf(
			"start_zapret: %s не запустилась — winws требует прав администратора", chosen.Name))
	}

	d.applyZapretState(true, chosen.Name)
	d.emitLog(LogInfo, fmt.Sprintf("zapret: включена %s", chosen.Name))

	resp, err := newResult(req.ID, struct {
		Active string `json:"active"`
	}{Active: chosen.Name})
	if err != nil {
		return newError(req.ID, err.Error())
	}
	return resp
}

// autoStartZapret brings the bypass up as part of connecting, when a bundle is
// installed.
//
// This is what makes the product one button. The user bought a VPN, pasted a
// link, dropped the bypass archive — expecting to press connect once and have
// YouTube and Discord work with no lag in games. Requiring them to also find and
// flip a second switch would put the assembly back on them.
//
// Failures here never block the connect: the tunnel alone still carries the
// censored services (that is what UnblockServices is for), so a bypass that will
// not start degrades the result rather than denying it. It is logged, not
// raised.
//
// Returns whether the bypass ended up running, which decides where the censored
// services are routed.
func (d *Daemon) autoStartZapret(ctx context.Context, tunnelUp bool) bool {
	d.zapretOpMu.Lock()
	defer d.zapretOpMu.Unlock()

	dir := filepath.Join(d.store.Dir(), zapretDirName)
	entries, err := os.ReadDir(dir)
	if err != nil {
		// Debug, not info: the caller already states at info where the censored
		// services ended up. This only adds which of the two ways it got there —
		// no bundle at all, as opposed to a bundle that would not start.
		d.emitDebug(fmt.Sprintf("zapret: сборки нет в %s — поднимать нечего", dir))
		return false
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	strategies := zapret.Discover(dir, names)
	if len(strategies) == 0 {
		d.emitLog(LogWarn, fmt.Sprintf("zapret: в %s нет ни одной стратегии — обход остаётся выключенным", dir))
		return false
	}

	d.mu.Lock()
	want := d.zapretActive
	d.mu.Unlock()

	chosen := strategies[0]
	found := false
	for _, s := range strategies {
		if s.Name == want {
			chosen, found = s, true
			break
		}
	}
	// Which strategy a connect ends up on is a choice with three different
	// causes, and they look identical from outside: the saved one, the bundle's
	// first because nothing was saved, and the bundle's first because the saved
	// one is gone after an update. Only the third is a problem, and it is exactly
	// the one that used to be invisible.
	var picked string
	switch {
	case found:
		picked = "выбрана раньше и сохранена"
	case want != "":
		picked = fmt.Sprintf("сохранённой %q в сборке больше нет, беру первую", want)
	default:
		picked = "выбранной стратегии не записано, беру первую"
	}
	d.emitDebug(fmt.Sprintf("zapret: %d стратегий в сборке, беру %s (%s)", len(strategies), chosen.Name, picked))

	d.excludeNodesFromZapret(dir)

	runner := d.newZapretRunnerFor(dir, tunnelUp)
	started, err := runner.Start(ctx, chosen)
	if err != nil || !started {
		// The usual cause is elevation: WinDivert loads a driver, which an
		// unprivileged process cannot do. Say so rather than leaving a silent gap
		// between "connected" and "YouTube still blocked".
		d.emitLog(LogWarn, fmt.Sprintf(
			"zapret: %s не запустилась — обход выключен, туннель работает без него", chosen.Name))
		return false
	}

	// The routing flag is set by the caller (handleConnect), which is mid-connect
	// and rebuilds the config immediately after — going through applyZapretState
	// here would fire a reapplyLive into a connect that has not happened yet. Only
	// the choice is recorded.
	d.mu.Lock()
	d.zapretActive = chosen.Name
	d.routing.ZapretCovered = zapret.Covered(dir)
	d.routing = d.routing.Normalize()
	d.mu.Unlock()
	d.persistSettings()
	d.emitLog(LogInfo, fmt.Sprintf("zapret: включена %s — YouTube и Discord идут напрямую", chosen.Name))
	return true
}

// stopZapretQuietly stops the bypass without touching routing or reporting, for
// shutdown: there is no live tunnel left to re-apply to and no client left to
// tell. It is a no-op when no bundle was ever installed.
//
// The stop is bounded: Runner.Stop waits for the packet filter to detach, and a
// wait that outlived shutdown would hang the process on exit.
func (d *Daemon) stopZapretQuietly() {
	d.zapretOpMu.Lock()
	defer d.zapretOpMu.Unlock()

	dir := filepath.Join(d.store.Dir(), zapretDirName)
	if _, err := os.Stat(dir); err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	_ = zapret.NewRunner(dir).Stop(ctx)
}

// handleStopZapret turns the bypass off.
//
// The picked strategy is kept, not cleared: turning the bypass off is not a
// decision to un-pick the strategy that was measured to work, and a later
// start_zapret with no name must bring back that one rather than the bundle
// default. Routing is told, so the censored services return to the tunnel
// instead of being left pointing at the direct path the bypass no longer covers.
func (d *Daemon) handleStopZapret(ctx context.Context, req Request) Response {
	d.zapretOpMu.Lock()
	defer d.zapretOpMu.Unlock()

	runner := zapret.NewRunner(filepath.Join(d.store.Dir(), zapretDirName))
	if err := runner.Stop(ctx); err != nil {
		return newError(req.ID, err.Error())
	}
	d.applyZapretState(false, "")
	d.emitLog(LogInfo, "zapret: выключен")
	return newResult0(req.ID)
}
