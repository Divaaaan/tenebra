package control

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Divaaaan/tenebra/core/dnswire"
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
	d.applyZapretToStateLocked(&d.state)
}

// applyZapretToStateLocked stamps the bypass's state onto any State value.
// Callers must hold d.mu.
//
// It exists apart from refreshZapretStateLocked because setState replaces the
// whole State rather than editing the live one, and these four fields are
// daemon-wide in exactly the way the split and DNS preferences are: they belong
// on every snapshot, not only on the ones written by a bypass command. Without
// this stamp a connect blanked them — the answer to the connect command is a
// snapshot taken after setState, so the bypass raised moments earlier on the
// same connect reported itself as absent, and nothing put it back for the rest
// of the session. applySettingsToState cannot do it: three of the four live in
// the daemon's own bookkeeping (the picked strategy, the updater bit, the
// version on disk), not in the routing options it is handed.
func (d *Daemon) applyZapretToStateLocked(s *State) {
	s.ZapretActive = d.routing.ZapretActive
	s.ZapretStrategy = d.zapretActive
	s.ZapretAutoUpdate = d.zapretAutoUpdate
	s.ZapretVersion = zapret.Version(filepath.Join(d.store.Dir(), zapretDirName))
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
	// End the run at the first strategy that carries every target. Both callers —
	// the button and the automatic re-pick after a bypass failure — are asking
	// "which one do I use now", not "what does the whole bundle score", and both
	// run while the user is waiting: the button behind a progress bar, the re-pick
	// with the censored services parked in the tunnel until it finishes. The
	// bundle's default strategy is measured first (see zapret.Discover) and on a
	// remembered network the one that won here last is measured before that, so
	// the run usually ends on its first or second strategy instead of its
	// twenty-second.
	r.StopOnPerfect = true
	// Keep the filter off our own tunnel's adapter, and measure on the very
	// interface it is kept to. Both come out of one lookup (see pinAndProbeBind):
	// a filter confined to one link and a measurement taken on another is the same
	// broken pick as no pin at all — every strategy is scored on a path the filter
	// never touches, so none of them beats the baseline.
	//
	// Pinned always, not only while connected: a strategy started before the
	// tunnel is still running after it comes up, and by then the damage is done
	// invisibly. The bind follows the pin, deliberately not tunnelUp, though that
	// answer is right here: a pick runs for minutes and a tunnel can come up
	// inside one, and with no tunnel at all a correct pin makes the bind a no-op
	// — the routed dial leaves by that interface anyway — so it costs nothing in
	// the case it is not needed and saves the run in the case it is.
	pin, bind := d.pinAndProbeBind()
	r.PinIfaceIndex = pin
	var reported sync.Once
	r.Dial = zapretProbeDialer(bind, func(err error) {
		// Once per runner: a pick dials five targets for every strategy in the
		// bundle, and twenty copies of one message is not a better log.
		reported.Do(func() {
			d.emitLog(LogWarn, fmt.Sprintf(
				"zapret: замер не привязался к интерфейсу (%v) — идёт по обычной маршрутизации", err))
		})
	}).DialContext
	d.emitDebug(fmt.Sprintf("zapret: фильтр на интерфейсе index=%d, замер %s", pin, bind.note))
	if pin > 0 && !bind.bound {
		// The filter IS confined to an interface and the measurement could not be
		// put on it, so a pick made in this state scores every strategy on a link
		// the filter never touches. Loud, because that is precisely the shape of
		// the original bug and this line is the only thing telling them apart in a
		// user's log.
		d.emitLog(LogWarn, fmt.Sprintf(
			"zapret: фильтр закреплён за интерфейсом index=%d, а замер к нему привязать не вышло — "+
				"подбор стратегии сейчас мерил бы другой канал", pin))
	}
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
	report, err := d.zapretExclude(dir, servers, d.nodeLookups())
	if err != nil {
		d.emitLog(LogWarn, fmt.Sprintf("zapret: не удалось исключить адреса узлов из фильтра: %v", err))
	}
	// A node addressed by name whose address we could not learn stays in the
	// filter's way, and the symptom is the one this whole function exists to
	// prevent: a port that opens and goes quiet, read as a dead node. Name the
	// nodes, so the failure can be traced to DNS instead of to the subscription.
	if len(report.Unresolved) > 0 {
		d.emitLog(LogWarn, fmt.Sprintf(
			"zapret: не удалось определить адреса узлов (%d): %s — фильтр может рвать подключение к ним",
			len(report.Unresolved), strings.Join(shortNameList(report.Unresolved), ", ")))
	}
	// Covered, but on an address DNS has not agreed with for hours. Not a warning
	// — the node is protected — but if the node has moved in the meantime, this
	// line is the only thing that says which ones were running on a memory.
	if len(report.FromCache) > 0 {
		d.emitDebug(fmt.Sprintf(
			"zapret: адреса узлов давно не подтверждались DNS (%d): %s — исключения держатся на прошлом ответе",
			len(report.FromCache), strings.Join(shortNameList(report.FromCache), ", ")))
	}
}

// nodeLookups are the resolvers a node name is asked through: the machine's own,
// and the direct resolver from the routing config.
//
// The second one is the point. sing-box resolves an outbound server name through
// its direct resolver (route.default_domain_resolver -> dns-direct), so that is
// the answer the tunnel will dial — and it is not necessarily the answer the
// system resolver gives. A censor that poisons the name poisons the system
// resolver, not the encrypted one; geo-DNS and round-robin hand different
// callers different records. Excluding the addresses of both is what makes the
// exclusion cover the address actually dialled, and an extra address in the list
// costs nothing but one address the filter leaves alone.
//
// A resolver whose transport this client cannot speak (DoQ) yields no second
// lookup, and the system one carries the load — the same coverage as before this
// existed, not less. That fallback says so in the log: it is the behaviour this
// function was written to replace, and silence about it leaves a user whose
// nodes are still being desynced with nothing to go on.
func (d *Daemon) nodeLookups() []zapret.Lookup {
	d.mu.Lock()
	direct := strings.TrimSpace(d.routing.DNSDirect)
	d.mu.Unlock()

	lookups := []zapret.Lookup{zapret.SystemLookup}
	if r, ok := dnswire.NewResolver(direct); ok {
		lookups = append(lookups, r.LookupIP)
	} else if direct != "" {
		d.emitDebug(fmt.Sprintf(
			"zapret: транспорт резолвера %s не поддерживается — адреса узлов спрашиваются только у системного", direct))
	}
	return lookups
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

// installZapretIfMissing puts a bypass bundle on a machine that has none, so a
// first connect does not need one dragged in by hand.
//
// This is the difference between the product as described — paste a link, press
// one button — and one that first asks the user to find a release page, pick the
// right asset and unpack it. The bundle is not optional equipment: without it
// YouTube and Discord go through the tunnel at full round-trip latency, which is
// the thing the app exists to avoid.
//
// There are two sources, and the user's auto-update choice separates them.
// Upstream is tried first and is exactly what that choice governs: fetching a
// release is this app reaching the network on its own, and someone who turned
// automatic updates off asked it not to. The bundle compiled into this binary is
// the floor under that (see installEmbeddedZapretIfMissing) and is deliberately
// NOT governed by it — those bytes cost no request, they are the release this
// client was built and tested against, and the switch is about keeping the
// bundle current, not about whether a bypass exists at all. Someone who unticked
// it turned off updates, not the bypass.
//
// So every way the download can end without a bundle — no network, GitHub
// blocked, a version no pin covers, an archive whose checksum did not match, or
// the user having declined the download outright — lands on the same fallback,
// and a first connect stops depending on GitHub being reachable at all.
//
// Bounded and best-effort by construction. A failure here is not a failed
// connect: the tunnel alone still carries everything, just slower, and the log
// says why.
func (d *Daemon) installZapretIfMissing(ctx context.Context) {
	dir := filepath.Join(d.store.Dir(), zapretDirName)
	if len(zapret.Discover(dir, dirFileNames(dir))) > 0 {
		return
	}
	d.mu.Lock()
	allowed := d.zapretAutoUpdate
	d.mu.Unlock()

	if allowed {
		d.emitLog(LogInfo, "zapret: сборки нет — ставлю сам")
		fetchCtx, cancel := context.WithTimeout(ctx, zapretInstallBudget)
		defer cancel()
		if from, to, ok, err := d.updateZapret(fetchCtx); err != nil {
			// Each branch reports what upstream did and stops there. What the machine
			// is left running is not decided here any more: the embedded floor below
			// answers that, and a line claiming the tunnel is on its own would be a
			// guess made one statement too early.
			switch {
			case errors.Is(err, zapret.ErrIntegrity):
				// A bundle that failed verification is not "could not download": the
				// archive arrived and was not what upstream published. Everything else on
				// this path is a connectivity problem the user can ignore; this one is not.
				// `from` is empty here by construction — nothing is installed — which is
				// what keeps the line off claiming a previous bundle was kept.
				d.reportZapretUpdateFailure(err, from)
			case errors.Is(err, zapret.ErrUntrustedVersion):
				// Upstream has moved past every pin this build carries, so there is no
				// bundle it can install on trust. This is the case that emptied fresh
				// installs when 1.10.2 was published ahead of its pin, and the one the
				// embedded copy exists for.
				d.emitLog(LogInfo, fmt.Sprintf(
					"zapret: доступна сборка обхода %s, но она новее вшитых в Tenebra проверок — "+
						"обнови Tenebra, чтобы получить её", versionLabel(to)))
			default:
				d.emitLog(LogWarn, fmt.Sprintf("zapret: не удалось скачать сборку (%v)", err))
			}
		} else if ok {
			d.emitLog(LogInfo, fmt.Sprintf("zapret: поставлена сборка %s", to))
		}
	}

	d.installEmbeddedZapretIfMissing(dir)
}

// installEmbeddedZapretIfMissing installs the bundle compiled into this binary
// when nothing else has left one on the machine.
//
// It is the floor and only the floor. The check is re-run against the directory
// rather than inferred from what updateZapret returned, so a bundle that did get
// installed — by the download just now, or sitting there all along — is never
// written over: the embedded copy is older than upstream by construction, and
// laying it on top of a working install would be a downgrade dressed as a repair.
//
// Nothing is fetched and nothing is verified here. These bytes came in the same
// signed binary as this code, their checksum is matched against the pin table on
// every build, and they are unpacked by the same install path a verified download
// uses.
func (d *Daemon) installEmbeddedZapretIfMissing(dir string) {
	// Serialized with every other operation that writes the bundle directory, for
	// the same reason updateZapret is: unpacking the embedded copy stages into
	// dir+".new" and swaps it into place — the very paths zapret.Apply uses — so an
	// update running alongside it would have the two deleting each other's staging
	// directory mid-unpack and could leave dir holding files of two releases under
	// one version stamp. Neither -race nor a test sees that: it is a race on the
	// file system, not on memory.
	//
	// Taken here and not by the caller: installZapretIfMissing reaches this only
	// after updateZapret has taken this lock and given it back, so there is nobody
	// above holding it. Inside, d.mu is taken and released the same way every other
	// bypass operation does it — zapretOpMu first, mu second, mu never held across
	// the call — so the order cannot invert.
	d.zapretOpMu.Lock()
	defer d.zapretOpMu.Unlock()

	// Re-checked under the lock, not before it. An update that finished while this
	// was waiting has already left a bundle in place, and the floor has nothing
	// left to hold up.
	if len(zapret.Discover(dir, dirFileNames(dir))) > 0 {
		return
	}

	strategies, err := d.zapretEmbed(dir)
	if err != nil {
		if errors.Is(err, zapret.ErrNoEmbeddedBundle) {
			// A build that carries no bundle: the bypass is a Windows packet filter and
			// this is macOS or Linux, where the tunnel carries the censored services by
			// itself. Nothing failed, so nothing is said — a warning on every connect
			// about a feature that does not exist on the platform is noise.
			return
		}
		d.emitLog(LogWarn, fmt.Sprintf(
			"zapret: не встала и вшитая сборка (%v) — туннель работает без обхода", err))
		return
	}

	// The installed version is part of the reported status, and a screen still
	// showing none leaves the user unable to tell a bypass that installed itself
	// from one that never arrived.
	d.mu.Lock()
	d.refreshZapretStateLocked()
	autoUpdate := d.zapretAutoUpdate
	d.mu.Unlock()

	// What happens to this bundle next depends on the switch the user set, and the
	// line has to promise only what will actually happen: with updates off nothing
	// will replace it on its own, and saying otherwise makes the log a liar to the
	// one user who read the setting carefully.
	next := "как выйдет более свежая с вшитой суммой, обновлю"
	if !autoUpdate {
		next = "автообновление выключено — более свежую поставлю по кнопке «Обновить»"
	}
	d.emitLog(LogInfo, fmt.Sprintf(
		"zapret: поставлена вшитая сборка %s (%d стратегий) — обход поднимется без GitHub; %s",
		zapret.EmbeddedVersion, len(strategies), next))
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

// pickRunner is the one operation a probe run needs from the bypass runner:
// measure every strategy, reporting each as it lands.
//
// It is an interface so the run's bookkeeping — the numbering behind the
// progress event — can be tested without a packet filter. The real Pick starts
// winws once per strategy on the WinDivert driver, which is neither available
// nor acceptable in a test.
type pickRunner interface {
	Pick(ctx context.Context, strategies []zapret.Strategy, targets []string, progress func(zapret.Result)) ([]zapret.Result, int, error)
}

// runPick measures every strategy and narrates the run as pick_progress events.
//
// The narration is the point. A run takes minutes — every strategy is attached,
// probed against each destination and detached — and until this existed the only
// sign of life was a line on the log screen, nowhere near the button that started
// it. A control that says "measuring" for five minutes with nothing behind it is
// indistinguishable from a hung app, and that is exactly how it was read.
//
// The numbering is done here rather than inside Pick because only this side knows
// the plan: the progress callback sees one result at a time, while the length of
// the run is right here. Widening Pick's signature to carry it would change a
// call the automatic re-pick shares (see repickStrategy) for the benefit of one
// caller.
func (d *Daemon) runPick(ctx context.Context, runner pickRunner, strategies []zapret.Strategy, targets []string) ([]zapret.Result, int, error) {
	total := len(strategies)
	// The opening beat goes out before anything is measured: the baseline probe
	// runs first and costs as much as a strategy does, so a run that only spoke up
	// after the first strategy landed would open with the same silence.
	d.emitPickProgress(pickProgressEvent{Targets: len(targets), Total: total})

	measured := 0
	return runner.Pick(ctx, strategies, targets, func(r zapret.Result) {
		measured++
		d.emitPickProgress(pickProgressEvent{
			Strategy: r.Name,
			OK:       r.OKCount(),
			Targets:  len(targets),
			Index:    measured,
			Total:    total,
		})
		// Kept beside the event rather than replaced by it: this line is what the
		// log screen and the diagnostics bundle carry, and both outlive the run.
		d.emitLog(LogInfo, fmt.Sprintf("zapret: %s — %d/%d", r.Name, r.OKCount(), len(r.Targets)))
	})
}

// emitPickProgress pushes one step of a probe run to the UI, if one is attached.
//
// Nothing is stored: a step is only meaningful while the run that produced it is
// still going, and the run is a synchronous request whose answer the caller is
// already waiting on. A client that attaches mid-run simply picks up from the
// next strategy.
func (d *Daemon) emitPickProgress(ev pickProgressEvent) {
	d.mu.Lock()
	emit := d.emit
	d.mu.Unlock()
	if emit != nil {
		emit(EventPickProgress, ev)
	}
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

	targets := zapret.DefaultTargets()
	runner := d.newZapretRunner(dir)
	results, baseline, err := d.runPick(ctx, runner, strategies, targets)
	if err != nil {
		return newError(req.ID, err.Error())
	}

	best, found := zapret.Best(results, baseline)
	ranked := zapret.Rank(results)

	// The baseline is what every strategy is scored against, so it is the first
	// number to look at when a run reports nothing: full marks with the bypass off
	// means the measurement never went anywhere the censor is, and the verdict
	// says nothing about the strategies. Paired with the interface it was measured
	// on, because that is the thing that goes wrong.
	d.emitDebug(fmt.Sprintf("zapret: без обхода %d/%d, замер на интерфейсе index=%d",
		baseline, len(targets), runner.PinIfaceIndex))

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
		Targets:  len(targets),
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
