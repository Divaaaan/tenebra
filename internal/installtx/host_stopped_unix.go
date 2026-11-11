//go:build linux || darwin

package installtx

import (
	"context"
	"math"
)

func hostStopPhase(j Journal) bool {
	return j.Validate() == nil && j.Transaction != nil && oneOf(j.Transaction.Phase, "fencing", "fenced", "schema_ready", "ready", "committed_closed", "rolling_back", "rolled_back", "removed", "blocked")
}

// Only HostedServiceManager calls this AFTER its trusted fixed native job has
// confirmed the entire job stopped and the independent host owner lock is held.
// No PID is invented or signalled. A reserved fork with no saved PID may be
// retired only through that explicit OS-owner recovery, never by a host reboot.
func (p *ProcessRecordFile) resetLaunchAfterStopped(ctx context.Context, journal JournalReader, expected LaunchRecord) error {
	if p == nil || p.root == nil || journal == nil || expected.Generation == math.MaxUint64 || expected.Generation != 0 && expected.Validate() != nil {
		return ErrProtocol
	}
	lock, err := p.root.LockWriter(ctx)
	if err != nil {
		return err
	}
	defer lock.Close()
	current, err := p.Read(ctx)
	if err != nil {
		return err
	}
	launch, err := p.ReadLaunch(ctx)
	if err != nil {
		return err
	}
	if current.Current != nil || !sameHostMetadata(launch, expected) {
		return ErrConflict
	}
	j, err := journal.ReadJournal(ctx)
	if err != nil {
		return err
	}
	if !hostStopPhase(j) {
		return ErrRecoveryRequired
	}
	if launch.State == "idle" {
		return nil
	}
	_, err = p.writeLaunch(ctx, LaunchRecord{Schema: 1, Generation: launch.Generation + 1, State: "idle"})
	return err
}
