package control

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/Divaaaan/tenebra/core/model"
	"github.com/Divaaaan/tenebra/core/profile"
	"github.com/Divaaaan/tenebra/core/singbox"
)

// The external boundaries are fake: these tests never mutate the host proxy or
// start/stop the real engine or bypass. In particular, cleanup does not use Close.
func proxySafetyDaemon(t *testing.T) (*Daemon, *fakeRunner, profile.Profile) {
	t.Helper()
	s, err := profile.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p, err := profile.NewProfile("proxy safety", profile.SourceManual, "", []model.Node{{
		Protocol: model.VLESS, Name: "fixture", Server: "192.0.2.1", Port: 443,
		UUID: "123e4567-e89b-12d3-a456-426614174000",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Add(p); err != nil {
		t.Fatal(err)
	}
	r := newFakeRunner()
	d := NewDaemon(s, r)
	d.localAddrs = func() []net.Addr { return nil }
	d.tunWatchInterval, d.healthInterval, d.bypassVerifyDelay = 0, 0, 0
	d.proxy = &fakeProxyController{}
	d.probeWarmup, d.probeRetry = time.Millisecond, time.Millisecond
	d.probeTimeout, d.probeBudget = time.Second, time.Second
	t.Cleanup(func() {
		d.connMu.Lock()
		d.teardown(StateIdle, "", "")
		d.connMu.Unlock()
		d.relaunchWG.Wait()
		d.entCancel()
	})
	return d, r, p
}

func TestProxyApplyFailureDoesNotPublishConnected(t *testing.T) {
	d, r, p := proxySafetyDaemon(t)
	f := &fakeProxyController{enableErr: errors.New("user proxy apply denied")}
	d.proxy = f
	d.tun.Mode = singbox.ModeSystemProxy
	d.connMu.Lock()
	_, err := d.startConnect(context.Background(), p, p.Servers[0].ID, false, false, "")
	d.connMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		st := d.snapshotState()
		if st.State == StateConnected {
			t.Fatal("published connected despite failed OS proxy apply")
		}
		if st.State == StateError {
			if !strings.Contains(st.Error, "system proxy") {
				t.Fatalf("unhelpful error: %q", st.Error)
			}
			if r.stops() == 0 {
				t.Fatal("unused engine left running after proxy failure")
			}
			if f.disables() != 1 {
				t.Fatalf("rollback calls = %d, want 1", f.disables())
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("proxy failure never reached error state")
}

func TestProxyCleanupFailureRetainsOwnershipUntilRetrySucceeds(t *testing.T) {
	d, _, _ := proxySafetyDaemon(t)
	f := &fakeProxyController{disableErr: errors.New("temporary cleanup failure")}
	d.proxy = f
	d.armSystemProxy("127.0.0.1:2080")
	d.disarmSystemProxy()
	d.mu.Lock()
	pending := d.proxyArmed
	d.mu.Unlock()
	if !pending {
		t.Fatal("cleanup failure discarded ownership")
	}
	f.mu.Lock()
	f.disableErr = nil
	f.mu.Unlock()
	d.disarmSystemProxy()
	d.disarmSystemProxy()
	if f.disables() != 2 {
		t.Fatalf("cleanup attempts=%d, want failed + successful", f.disables())
	}
	d.mu.Lock()
	pending = d.proxyArmed
	d.mu.Unlock()
	if pending {
		t.Fatal("successful cleanup did not release ownership")
	}
}

func TestPartialProxyApplyRollsBackBeforeReportingFailure(t *testing.T) {
	d, _, _ := proxySafetyDaemon(t)
	f := &fakeProxyController{enableErr: errors.New("refresh failed after registry write")}
	d.proxy = f
	d.armSystemProxy("127.0.0.1:2080")
	if f.disables() != 1 {
		t.Fatal("potentially partial application was not rolled back")
	}
	d.disarmSystemProxy()
	if f.disables() != 1 {
		t.Fatal("successful rollback was repeated")
	}
}
