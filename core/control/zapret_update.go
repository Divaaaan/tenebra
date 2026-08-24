package control

import (
	"context"
	"errors"
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
			if from, to, updated, err := d.updateZapret(ctx); err != nil {
				d.reportZapretUpdateOutcome(from, to, err)
			} else if updated {
				d.emitLog(LogInfo, "zapret: сборка обновлена")
			}
		}

		timer.Reset(zapretUpdateInterval)
	}
}

// reportZapretUpdateOutcome logs the result of a background update attempt that
// did not install anything, at the volume it deserves. It layers the "there is a
// newer bundle this client does not yet trust" case on top of the
// failure/ordinary split in reportZapretUpdateFailure.
//
// That case is neither a failure nor an alarm: upstream shipped a version newer
// than any checksum pinned into this build, so the safe move is to keep the
// working bundle and tell the user to update Tenebra, which carries the next pin.
// It is logged quietly at info level and names the version, so the line is
// actionable rather than noise.
//
// installed is the version already on disk, empty when there is none. It decides
// nothing here and is passed straight through, because what a refused download
// leaves the machine running is the one thing the failure line has to get right.
func (d *Daemon) reportZapretUpdateOutcome(installed, latest string, err error) {
	if errors.Is(err, zapret.ErrUntrustedVersion) {
		d.emitLog(LogInfo, fmt.Sprintf(
			"zapret: доступна новая сборка обхода %s, но она новее вшитых в Tenebra проверок — "+
				"обнови Tenebra, чтобы получить её (обход пока работает на текущей сборке).", versionLabel(latest)))
		return
	}
	d.reportZapretUpdateFailure(err, installed)
}

// reportZapretUpdateFailure puts a failed bundle update on the log channel at the
// volume it deserves.
//
// The two cases are different events, not two severities of one. A check that
// could not reach the release feed is ordinary — the network is down, GitHub is
// blocked, the retry is in twelve hours — and logging that as a problem trains
// the user to scroll past the log. An archive that arrived and did not verify
// means something between the release page and this machine rewrote bytes that
// were about to be unpacked into the daemon's directory, where a .bat out of them
// gets handed to cmd.exe by a service running as LocalSystem. Nothing else this
// updater can report comes close, so nothing else gets this level.
//
// installed is the bundle version on disk, empty when there is none, and it
// picks which half of the alarm is true. A refused update leaves the working
// bundle exactly where it was; a refused first install leaves nothing behind it
// — telling that user a previous bundle was kept describes a machine other than
// theirs, and it is followed by the embedded floor going in, which would make the
// two lines contradict each other.
func (d *Daemon) reportZapretUpdateFailure(err error, installed string) {
	if errors.Is(err, zapret.ErrIntegrity) {
		kept := "сборка обхода НЕ установлена, осталась прежняя"
		if installed == "" {
			kept = "скачанная сборка обхода НЕ установлена"
		}
		d.emitLog(LogError, fmt.Sprintf(
			"%v. Скачанный архив не совпал с опубликованным — %s. "+
				"Если это повторяется, сеть между тобой и GitHub подменяет файлы.", err, kept))
		return
	}
	d.emitLog(LogInfo, fmt.Sprintf("zapret: проверка обновления не удалась: %v", err))
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

	// A newer bundle exists, but auto-install is limited to versions this client
	// pins a checksum for. The digest GitHub publishes beside the asset cannot
	// stand in: it rides the same TLS connection as the archive, so the
	// trusted-root proxy this product defends against forges both together. So an
	// unpinned version is reported (ErrUntrustedVersion, which the callers turn
	// into an "update Tenebra" notice) and nothing else happens — no download, and
	// the running bypass is left alone rather than being stopped for an install
	// that will not come. The pin ships with the next Tenebra release.
	if zapret.PinnedSum(rel.Version) == "" {
		return from, rel.Version, false, zapret.ErrUntrustedVersion
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
	// A version newer than any pin is not something the user can fix by retrying:
	// it is "there is an update, but this Tenebra build does not trust it yet".
	// Report it as a normal result flagged blocked, so the screen can say "update
	// Tenebra" rather than show a red failure the way a real refusal would.
	if err != nil && !errors.Is(err, zapret.ErrUntrustedVersion) {
		return newError(req.ID, err.Error())
	}
	resp, mErr := newResult(req.ID, struct {
		Installed string `json:"installed"`
		Latest    string `json:"latest"`
		Updated   bool   `json:"updated"`
		Blocked   bool   `json:"blocked"`
	}{Installed: from, Latest: to, Updated: updated, Blocked: errors.Is(err, zapret.ErrUntrustedVersion)})
	if mErr != nil {
		return newError(req.ID, mErr.Error())
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
