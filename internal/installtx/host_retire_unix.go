//go:build linux || darwin

package installtx

import (
	"context"
	"encoding/json"
	"math"
)

// Retirement never signals a metadata PID. Prove the exact kernel lifetime
// ended BEFORE taking the short metadata writer. Reset the launch first: a
// crash between writes leaves current.json blocking another fork, safely.
func (p *ProcessRecordFile) retireEnded(ctx context.Context, expected ProcessRecord, launch LaunchRecord) error {
	if p == nil || p.root == nil || expected.Validate() != nil || expected.Current == nil ||
		expected.Generation == math.MaxUint64 || launch.Generation == math.MaxUint64 ||
		launch.Generation != 0 && launch.Validate() != nil ||
		launch.Generation == 0 && (launch.Schema != 1 || launch.State != "idle" || launch.Request != nil || launch.OperationalCommitID != "") {
		return ErrProtocol
	}
	if launch.Request != nil && (launch.Request.TxID != expected.Current.TxID || launch.Request.LaunchID != expected.Current.LaunchID || launch.Request.Runtime != expected.Current.Runtime) {
		return ErrConflict
	}
	if err := ProcessDefinitelyEnded(ctx, expected.Current.Identity()); err != nil {
		return err
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
	observed, err := p.ReadLaunch(ctx)
	if err != nil {
		return err
	}
	if !sameHostMetadata(current, expected) || !sameHostMetadata(observed, launch) {
		return ErrConflict
	}
	if launch.State != "idle" {
		if _, err := p.writeLaunch(ctx, LaunchRecord{Schema: 1, Generation: launch.Generation + 1, State: "idle"}); err != nil {
			return err
		}
	}
	_, err = p.writeProcess(ctx, ProcessRecord{Schema: 1, Generation: current.Generation + 1})
	return err
}

func sameHostMetadata(a, b any) bool {
	x, err := json.Marshal(a)
	if err != nil {
		return false
	}
	y, err := json.Marshal(b)
	return err == nil && string(x) == string(y)
}
