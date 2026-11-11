package protection

import (
	"errors"
	"sync"
	"testing"
)

func TestGuardSnapshotWithEpochIsCoherent(t *testing.T) {
	g := New(nil)
	initial, epoch := g.SnapshotWithEpoch()
	if epoch != 1 || initial.Status != "unavailable" {
		t.Fatalf("initial snapshot: %#v epoch %d", initial, epoch)
	}
	want := State{Status: "blocked", Enforced: true, Persistent: true}
	g.confirm(want, nil)
	got, next := g.SnapshotWithEpoch()
	if got != want || next <= epoch {
		t.Fatalf("confirmed snapshot: %#v epoch %d, previous %d", got, next, epoch)
	}
	_, same := g.SnapshotWithEpoch()
	if same != next {
		t.Fatal("reading state changed its epoch")
	}
	_ = g.Reject(errors.New("synthetic refusal"))
	got, failed := g.SnapshotWithEpoch()
	if failed <= next || got.Status != "error" || !got.Enforced || !got.Persistent {
		t.Fatalf("failure lost confirmed enforcement or epoch: %#v %d", got, failed)
	}
}

func TestGuardEpochCapturedBeforeNotify(t *testing.T) {
	g := New(nil)
	arrived, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	g.SetNotify(func() {
		once.Do(func() { close(arrived) })
		<-release
	})
	done := make(chan struct{})
	go func() { defer close(done); g.confirm(State{Status: "off"}, nil) }()
	<-arrived
	snapshot, epoch := g.SnapshotWithEpoch()
	if snapshot.Status != "off" || epoch != 2 {
		t.Errorf("notification observed an unstamped view: %#v %d", snapshot, epoch)
	}
	close(release)
	<-done
}

func TestGuardEpochTracksAcceptanceAndInterruption(t *testing.T) {
	g := New(nil)
	g.confirm(State{Status: "blocked", Enforced: true, Persistent: true}, &verifiedTunnel{luid: 1})
	_, before := g.SnapshotWithEpoch()
	g.Accepted()
	active, accepted := g.SnapshotWithEpoch()
	if active.Status != "active" || accepted <= before {
		t.Fatalf("acceptance missing epoch: %#v %d, previous %d", active, accepted, before)
	}
	g.Interrupted()
	blocked, interrupted := g.SnapshotWithEpoch()
	if blocked.Status != "blocked" || interrupted <= accepted {
		t.Fatalf("interruption missing epoch: %#v %d, previous %d", blocked, interrupted, accepted)
	}
	g.Interrupted()
	_, repeated := g.SnapshotWithEpoch()
	if repeated != interrupted {
		t.Fatal("no-op interruption changed the epoch")
	}
}
