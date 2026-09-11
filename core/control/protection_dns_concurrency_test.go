package control

import (
	"sync"
	"testing"
	"time"

	"github.com/Divaaaan/tenebra/core/protection"
)

func TestHostProtectionDNSValidationSerializesWithOn(t *testing.T) {
	d, _, _ := coreAuditDaemon(t)
	d.SetProtection(protection.New(&fakeHostProtection{}))
	before := d.snapshotRouting().DNSDirect

	// ON owns protectionOp while it validates/applies policy. A DNS command
	// arriving then must validate against the eventual ON value, not an earlier
	// unprotected snapshot. This models the setting boundary without native I/O.
	d.protectionOp.Lock()
	var unlockOnce sync.Once
	unlock := func() { unlockOnce.Do(d.protectionOp.Unlock) }
	defer unlock()
	started := make(chan struct{})
	done := make(chan Response, 1)
	go func() {
		close(started)
		done <- d.handleSetDNS(Request{ID: 1, DNSDirect: "udp://192.0.2.53"})
	}()
	<-started
	select {
	case resp := <-done:
		t.Fatalf("DNS setting crossed the ON critical section: %+v", resp)
	case <-time.After(20 * time.Millisecond):
	}
	d.mu.Lock()
	d.routing.KillSwitch = true
	d.mu.Unlock()
	unlock()
	select {
	case resp := <-done:
		if resp.Ok {
			t.Fatal("ON accepted a racing plaintext bootstrap")
		}
	case <-time.After(time.Second):
		t.Fatal("DNS command did not leave its critical section")
	}
	if got := d.snapshotRouting().DNSDirect; got != before {
		t.Fatalf("rejected DNS overwritten: %q, want %q", got, before)
	}
}
