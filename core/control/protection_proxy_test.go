package control

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Divaaaan/tenebra/core/protection"
	"github.com/Divaaaan/tenebra/core/singbox"
)

func TestHostProtectionProxyFailureCannotPublishActive(t *testing.T) {
	d, r, p := coreAuditDaemon(t)
	d.SetProtection(protection.New(&fakeHostProtection{}))
	d.routing.KillSwitch = true
	d.tun.Mode = singbox.ModeSystemProxy
	d.proxy = &fakeProxyController{enableErr: errors.New("interactive proxy denied")}
	var accepted atomic.Bool
	d.SetEmitter(func(name string, body any) {
		if state, ok := body.(stateEvent); name == EventState && ok {
			if state.State == StateConnected || state.Protection.Status == "active" {
				accepted.Store(true)
			}
		}
	})
	d.connMu.Lock()
	_, err := d.startConnect(context.Background(), p, "", false, false, "")
	d.connMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for d.snapshotState().State != StateError && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	s := d.snapshotState()
	if accepted.Load() || s.State != StateError || s.Protection.Status != "blocked" || !s.Protection.Enforced || r.starts() != 1 || !strings.Contains(s.Error, "interactive proxy denied") {
		t.Fatalf("accepted=%v starts=%d state=%+v", accepted.Load(), r.starts(), s)
	}
	if _, ok := d.lastGood.Get(p.ID); ok {
		t.Fatal("failed local proxy gate recorded last-good")
	}
}

func TestHostProtectionDisconnectReportsBothCleanupFailuresAndRetries(t *testing.T) {
	d, _, p := coreAuditDaemon(t)
	native := &fakeHostProtection{}
	d.SetProtection(protection.New(native))
	d.routing.KillSwitch = true
	d.tun.Mode = singbox.ModeSystemProxy
	proxy := &fakeProxyController{}
	d.proxy = proxy
	coreAuditConnect(t, d, p, "")
	proxy.mu.Lock()
	proxy.disableErr = errors.New("proxy rollback denied")
	proxy.mu.Unlock()
	native.mu.Lock()
	native.removeErr = errors.New("WFP removal denied")
	native.mu.Unlock()
	resp := d.handleDisconnect(Request{ID: 1})
	if resp.Ok || !strings.Contains(resp.Error, "proxy rollback denied") || !strings.Contains(resp.Error, "WFP removal denied") {
		t.Fatalf("cleanup failures lost: %+v", resp)
	}
	if s := d.snapshotState(); !s.Protection.Enforced || s.Protection.Status != "error" {
		t.Fatal(s)
	}
	proxy.mu.Lock()
	proxy.disableErr = nil
	proxy.mu.Unlock()
	native.mu.Lock()
	native.removeErr = nil
	native.mu.Unlock()
	if resp = d.handleDisconnect(Request{ID: 2}); !resp.Ok {
		t.Fatal(resp)
	}
	if s := d.snapshotState(); s.Protection.Enforced || s.Protection.Status != "off" || s.State != StateIdle {
		t.Fatal(s)
	}
}
