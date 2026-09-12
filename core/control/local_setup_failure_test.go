package control

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type fakeLocalSetupError struct{}

func (fakeLocalSetupError) Error() string           { return "engine lifetime job assignment denied" }
func (fakeLocalSetupError) LocalSetupFailure() bool { return true }

type localSetupFailRunner struct {
	*fakeRunner
	attempts atomic.Int32
}

func (r *localSetupFailRunner) Start(context.Context, []byte) error {
	r.attempts.Add(1)
	return fakeLocalSetupError{}
}

func TestLocalSetupFailureDoesNotMarkEveryServerUnavailable(t *testing.T) {
	d, base, p := proxySafetyDaemon(t)
	second := p.Servers[0]
	second.ID = "second-server"
	p.Servers = append(p.Servers, second)
	r := &localSetupFailRunner{fakeRunner: base}
	d.runner = r
	d.connMu.Lock()
	_, err := d.startConnect(context.Background(), p, "", false, false, "")
	d.connMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		st := d.snapshotState()
		if st.State == StateError {
			if !strings.Contains(st.Error, "job assignment denied") {
				t.Fatalf("local failure hidden as remote failure: %q", st.Error)
			}
			if got := r.attempts.Load(); got != 1 {
				t.Fatalf("retried %d servers for a machine-wide setup failure", got)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("local setup failure never surfaced")
}
