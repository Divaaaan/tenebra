package control

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Divaaaan/tenebra/core/zapret"
)

// Update cadence. The bundle is published every few weeks, so a check on start
// plus one twice a day is far more than enough to never run a stale bypass —
// and cheap enough (one API request) that it costs nothing when nothing changed.
//
// The startup delay keeps the check out of the way of the thing the user is
// actually waiting for: autoconnect fires at daemon start, and a download
// competing with the first handshake would make connecting look slow.
const (
	zapretUpdateStartupDelay = 45 * time.Second
	zapretUpdateInterval     = 12 * time.Hour
)

// RunZapretAutoUpdate keeps the DPI-bypass bundle current until ctx is done.
//
// Why this exists at all: the bypass is the one component whose value expires.
// The censor changes what it detects, upstream answers with new strategies, and
// a bundle that worked in March is simply a set of tricks the DPI has since
// learned. A user running a months-old bundle sees YouTube stop loading and has
// no way to tell that from a broken VPN, a dead node, or a new block — the
// symptom is identical and the cause is invisible.
//
// It runs in the background and never blocks anything: a failed check is logged
// and retried at the next tick.
func (d *Daemon) RunZapretAutoUpdate(ctx context.Context) {
	timer := time.NewTimer(zapretUpdateStartupDelay)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}

		d.mu.Lock()
		enabled := d.zapretAutoUpdate
		d.mu.Unlock()
		if enabled {
			if _, _, updated, err := d.updateZapret(ctx); err != nil {
				// Not a warning: an update check failing because the network is
				// down or GitHub is unreachable is an ordinary event, and logging
				// it as a problem would train the user to ignore the log.
				d.emitLog(LogInfo, fmt.Sprintf("zapret: проверка обновления не удалась: %v", err))
			} else if updated {
				d.emitLog(LogInfo, "zapret: сборка обновлена")
			}
		}

		timer.Reset(zapretUpdateInterval)
	}
}

// updateZapret installs the newest published bundle when it is newer than the
// installed one, and reports what happened.
//
// It also installs the bundle when there is none: the bypass is half the
// product, and requiring the user to find a release page, download an archive
// and drag it in is the assembly work this app exists to remove. Dropping an
// archive in by hand still works and still wins — it is simply no longer the
// only way to get one.
//
// The whole operation is serialized against every other bypass operation
// (probing, starting, stopping): they all drive the same single winws process
// and the same directory on disk, and two of them at once produce a bundle
// half-replaced under a running filter.
func (d *Daemon) updateZapret(ctx context.Context) (from, to string, updated bool, err error) {
	d.zapretOpMu.Lock()
	defer d.zapretOpMu.Unlock()

	dir := filepath.Join(d.store.Dir(), zapretDirName)
	from = zapret.Version(dir)

	rel, err := d.zapretLatest(ctx)
	if err != nil {
		return from, "", false, err
	}
	if !zapret.Newer(from, rel.Version) {
		return from, rel.Version, false, nil
	}

	// A running strategy pins the bundle directory on Windows, so the filter comes
	// down before the files move. What was running is remembered and put back
	// afterwards: the user turned the bypass on, and an update is not a reason to
	// leave it off.
	d.mu.Lock()
	wasRunning := d.routing.ZapretActive
	strategy := d.zapretActive
	d.mu.Unlock()

	runner := d.newZapretRunner(dir)
	if wasRunning {
		if stopErr := runner.Stop(ctx); stopErr != nil {
			return from, rel.Version, false, stopErr
		}
		d.emitLog(LogInfo, fmt.Sprintf("zapret: ставлю %s — обход выключен на время обновления", rel.Version))
	}

	if err := d.zapretApply(ctx, dir, rel); err != nil {
		// The old bundle is still in place (Apply stages and swaps), so putting the
		// bypass back is both possible and the right thing to do: the user asked
		// for it to be on, and a failed update must not silently leave it off.
		if wasRunning {
			d.restartZapretAfterUpdate(ctx, dir, strategy)
		}
		return from, rel.Version, false, err
	}

	// Republish the status: the installed version is part of it, and a UI that
	// keeps showing the old one leaves the user unable to tell whether the update
	// they were promised ever happened.
	d.mu.Lock()
	d.refreshZapretStateLocked()
	d.mu.Unlock()

	d.emitLog(LogInfo, fmt.Sprintf("zapret: сборка обновлена %s → %s", versionLabel(from), rel.Version))

	if wasRunning {
		d.restartZapretAfterUpdate(ctx, dir, strategy)
	}
	return from, rel.Version, true, nil
}

// versionLabel renders an unknown installed version readably.
func versionLabel(v string) string {
	if v == "" {
		return "неизвестной версии"
	}
	return v
}

// restartZapretAfterUpdate brings the bypass back on the new bundle.
//
// The strategy is matched by name. Upstream renames and retires strategies
// between releases, so the remembered one may be gone; falling back to the
// bundle default keeps the bypass running, and the log says plainly that the
// pick no longer exists — silently running a different strategy under the old
// name would make the next "why did YouTube stop working" unanswerable.
func (d *Daemon) restartZapretAfterUpdate(ctx context.Context, dir, strategy string) {
	strategies := discoverStrategies(dir)
	if len(strategies) == 0 {
		d.emitLog(LogWarn, "zapret: в обновлённой сборке нет стратегий — обход остался выключенным")
		d.applyZapretState(false, "")
		return
	}

	chosen := strategies[0]
	found := strategy == ""
	for _, s := range strategies {
		if s.Name == strategy {
			chosen, found = s, true
			break
		}
	}
	if !found {
		d.emitLog(LogWarn, fmt.Sprintf(
			"zapret: стратегии %q больше нет в сборке — включаю %s, стоит переподобрать", strategy, chosen.Name))
	}

	d.excludeNodesFromZapret(dir)
	started, err := d.newZapretRunner(dir).Start(ctx, chosen)
	if err != nil || !started {
		d.emitLog(LogWarn, fmt.Sprintf("zapret: %s не запустилась после обновления", chosen.Name))
		d.applyZapretState(false, chosen.Name)
		return
	}
	d.applyZapretState(true, chosen.Name)
	d.emitLog(LogInfo, fmt.Sprintf("zapret: включена %s", chosen.Name))
}

// discoverStrategies lists the strategies of an installed bundle, or nothing
// when none is installed.
func discoverStrategies(dir string) []zapret.Strategy {
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
	return zapret.Discover(dir, names)
}

// handleUpdateZapret checks for a newer bundle on demand and installs it.
//
// The manual command exists beside the automatic loop because the automatic one
// is invisible: when YouTube stops loading, "check for a new bypass now" is the
// first thing a user wants to do, and waiting up to twelve hours for the timer
// is not an answer.
func (d *Daemon) handleUpdateZapret(ctx context.Context, req Request) Response {
	from, to, updated, err := d.updateZapret(ctx)
	if err != nil {
		return newError(req.ID, err.Error())
	}
	resp, err := newResult(req.ID, struct {
		Installed string `json:"installed"`
		Latest    string `json:"latest"`
		Updated   bool   `json:"updated"`
	}{Installed: from, Latest: to, Updated: updated})
	if err != nil {
		return newError(req.ID, err.Error())
	}
	return resp
}

// handleSetZapretAutoUpdate arms or disarms automatic bundle updates. The choice
// is recorded, persisted and reported; the background loop reads the flag at its
// next tick, so a toggle takes effect without a restart.
func (d *Daemon) handleSetZapretAutoUpdate(req Request) Response {
	d.mu.Lock()
	d.zapretAutoUpdate = req.On
	d.refreshZapretStateLocked()
	d.mu.Unlock()

	d.persistSettings()

	resp, err := newResult(req.ID, d.snapshotState())
	if err != nil {
		return newError(req.ID, err.Error())
	}
	return resp
}
