package control

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"

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
	return zapretBundleResponse(req.ID, dir, strategies)
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

// handlePickZapret probes every installed strategy and reports which one to use.
//
// It runs synchronously and takes minutes: each strategy needs the packet filter
// attached, five control requests, and a clean detach before the next. That is
// the honest cost of the answer — the alternative is asking the user to try
// twenty batch files by hand.
func (d *Daemon) handlePickZapret(ctx context.Context, req Request) Response {
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

	runner := zapret.NewRunner(dir)
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
			d.mu.Lock()
			d.zapretActive = best.Name
			d.mu.Unlock()
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

	runner := zapret.NewRunner(dir)
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

	d.mu.Lock()
	d.zapretActive = chosen.Name
	d.mu.Unlock()
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
func (d *Daemon) autoStartZapret(ctx context.Context) bool {
	dir := filepath.Join(d.store.Dir(), zapretDirName)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false // nothing imported; the tunnel does the whole job
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	strategies := zapret.Discover(dir, names)
	if len(strategies) == 0 {
		return false
	}

	d.mu.Lock()
	want := d.zapretActive
	d.mu.Unlock()

	chosen := strategies[0]
	for _, s := range strategies {
		if s.Name == want {
			chosen = s
			break
		}
	}

	runner := zapret.NewRunner(dir)
	started, err := runner.Start(ctx, chosen)
	if err != nil || !started {
		// The usual cause is elevation: WinDivert loads a driver, which an
		// unprivileged process cannot do. Say so rather than leaving a silent gap
		// between "connected" and "YouTube still blocked".
		d.emitLog(LogWarn, fmt.Sprintf(
			"zapret: %s не запустилась — обход выключен, туннель работает без него", chosen.Name))
		return false
	}

	d.mu.Lock()
	d.zapretActive = chosen.Name
	d.mu.Unlock()
	d.emitLog(LogInfo, fmt.Sprintf("zapret: включена %s — YouTube и Discord идут напрямую", chosen.Name))
	return true
}

// handleStopZapret turns the bypass off.
func (d *Daemon) handleStopZapret(ctx context.Context, req Request) Response {
	runner := zapret.NewRunner(filepath.Join(d.store.Dir(), zapretDirName))
	if err := runner.Stop(ctx); err != nil {
		return newError(req.ID, err.Error())
	}
	d.mu.Lock()
	d.zapretActive = ""
	d.mu.Unlock()
	d.emitLog(LogInfo, "zapret: выключен")
	return newResult0(req.ID)
}