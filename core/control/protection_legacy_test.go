package control

import (
	"context"
	"testing"
	"time"
)

func TestHostProtectionLegacyEngineCompatibility(t *testing.T) {
	d, r, p := coreAuditDaemon(t)
	d.UseLegacyEngineProtection()
	d.routing.KillSwitch = true // existing saved preference, never migrated OFF
	d.routing.DNSDirect = "udp://192.0.2.53"
	state := coreAuditConnect(t, d, p, "")
	checkWarning := func() {
		t.Helper()
		state = d.snapshotState()
		if state.Protection.Status != "unavailable" || state.Protection.Enforced || state.Protection.Persistent || state.Protection.Error == "" {
			t.Fatalf("legacy routing claimed independent protection: %+v", state.Protection)
		}
		if stateEventBody(state).Protection != state.Protection {
			t.Fatal("state event lost unavailable explanation")
		}
	}
	checkWarning()
	if !state.KillSwitch {
		t.Fatal("saved preference was silently migrated OFF")
	}
	if endpoint, strict := d.ProtectionDNS(); strict || endpoint != "udp://192.0.2.53" {
		t.Fatalf("legacy DNS choice changed: endpoint=%q strict=%t", endpoint, strict)
	}
	if strict, _ := tunFromConfig(t, r.startCfgs()[0]); !strict {
		t.Fatal("legacy strict_route disappeared")
	}
	for i, on := range []bool{false, true} {
		if resp := d.handleSetKillSwitch(Request{ID: int64(i + 1), On: on}); !resp.Ok {
			t.Fatal(resp)
		}
		deadline := time.Now().Add(time.Second)
		for d.snapshotState().State != StateConnected && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		checkWarning()
		if state.State != StateConnected || state.KillSwitch != on {
			t.Fatal(state)
		}
		cfgs := r.startCfgs()
		if strict, _ := tunFromConfig(t, cfgs[len(cfgs)-1]); strict != on {
			t.Fatalf("legacy strict_route=%t, want %t", strict, on)
		}
	}
	if resp := d.handleSetDNS(Request{ID: 3, DNSDirect: "udp://192.0.2.54"}); !resp.Ok {
		t.Fatal("legacy plaintext setting unexpectedly rejected", resp)
	}
	if endpoint, strict := d.ProtectionDNS(); strict || endpoint != "udp://192.0.2.54" {
		t.Fatal(endpoint, strict)
	}
}

func TestHostProtectionMissingBackendDoesNotInferLegacy(t *testing.T) {
	d, r, p := coreAuditDaemon(t)
	d.routing.KillSwitch = true
	d.connMu.Lock()
	_, err := d.startConnect(context.Background(), p, "", false, false, "")
	d.connMu.Unlock()
	if err == nil || r.starts() != 0 || d.snapshotState().Protection.Status != "unavailable" {
		t.Fatal("ordinary missing backend silently downgraded", err, r.starts())
	}
}
