package protection

import (
	"errors"
	"net/netip"
	"testing"
)

type packet struct {
	layer                Layer
	app                  string
	loop                 bool
	luid                 uint64
	proto, local, remote uint64
	addr                 string
}

func allowed(rules []Rule, p packet) bool {
	for _, r := range rules {
		if r.Layer != p.layer {
			continue
		}
		match := true
		for _, c := range r.Conditions {
			switch c.Field {
			case Loopback:
				match = match && p.loop
			case Interface:
				match = match && p.luid == c.Number
			case Application:
				match = match && p.app == c.Text
			case Protocol:
				match = match && p.proto == c.Number
			case LocalPort:
				match = match && p.local == c.Number
			case RemotePort:
				match = match && p.remote == c.Number
			case RemoteAddress:
				a, err := netip.ParseAddr(p.addr)
				match = match && err == nil && netip.MustParsePrefix(c.Text).Contains(a)
			}
		}
		if match {
			return r.Permit
		}
	}
	return false
}

func TestPolicyBlocksOrdinaryPhysicalAndTrustedPlainDNS(t *testing.T) {
	rules := Policy(42)
	for _, layer := range Layers {
		p := packet{layer: layer, proto: 6, local: 50000, remote: 443}
		if allowed(rules, p) {
			t.Fatal("ordinary physical traffic permitted", layer)
		}
		p.app = Engine
		if !allowed(rules, p) {
			t.Fatal("engine transport blocked", layer)
		}
		if layer.Outbound() {
			p.remote = 53
		} else {
			p.local = 53
		}
		if allowed(rules, p) {
			t.Fatal("engine plaintext DNS escaped", layer)
		}
		p.luid = 42
		if !allowed(rules, p) {
			t.Fatal("DNS inside TUN blocked", layer)
		}
		p.luid = 43
		if allowed(rules, p) {
			t.Fatal("replacement uplink inherited TUN permission", layer)
		}
		p.loop = true
		if !allowed(rules, p) {
			t.Fatal("loopback blocked", layer)
		}
	}
}

func TestPolicyLockdownDHCPNDPAndDefaultCoverage(t *testing.T) {
	rules := Policy(0)
	seen := map[Layer]int{}
	for _, r := range rules {
		if len(r.Conditions) == 0 && !r.Permit {
			seen[r.Layer]++
		}
		for _, c := range r.Conditions {
			if c.Field == Interface {
				t.Fatal("lockdown has TUN permit")
			}
		}
	}
	for _, layer := range Layers {
		if seen[layer] != 1 {
			t.Fatal("missing unique default block", layer)
		}
		p := packet{layer: layer, app: DHCP, proto: 17, local: 68, remote: 67}
		if layer.IPv6() {
			p.local, p.remote = 546, 547
		}
		if !allowed(rules, p) {
			t.Fatal("DHCP unavailable", layer)
		}
		p.app = "browser"
		if allowed(rules, p) {
			t.Fatal("untrusted DHCP-port bypass", layer)
		}
	}
	for _, addr := range []string{"fe80::1", "ff02::1"} {
		if !allowed(rules, packet{layer: Connect6, proto: 58, local: 135, remote: 0, addr: addr}) {
			t.Fatal("NDP blocked")
		}
	}
	if allowed(rules, packet{layer: Connect6, proto: 58, local: 128, remote: 0, addr: "fe80::1"}) {
		t.Fatal("arbitrary ICMP allowed")
	}
	if allowed(rules, packet{layer: Connect6, proto: 58, local: 135, remote: 0, addr: "2001:db8::1"}) {
		t.Fatal("offlink NDP allowed")
	}
}

type memoryBackend struct {
	present, complete   bool
	applyErr, removeErr error
	luid                uint64
	applications        []uint64
	removes             int
}

func (m *memoryBackend) Inspect() (bool, bool, error) { return m.present, m.complete, nil }
func (m *memoryBackend) Replace(luid uint64) error {
	m.applications = append(m.applications, luid)
	if m.applyErr != nil {
		return m.applyErr
	}
	m.present, m.complete = true, true
	return nil
}
func (m *memoryBackend) ResolveTunnel(string, string) (uint64, error) {
	if m.luid == 0 {
		return 0, errors.New("missing TUN")
	}
	return m.luid, nil
}
func (m *memoryBackend) Remove() error {
	m.removes++
	if m.removeErr != nil {
		return m.removeErr
	}
	m.present, m.complete = false, false
	return nil
}

func TestGuardFailureAndReleasePreserveTruth(t *testing.T) {
	b := &memoryBackend{luid: 42}
	g := New(b)
	if err := g.Prepare(); err != nil {
		t.Fatal(err)
	}
	if s := g.Snapshot(); s.Status != "blocked" || !s.Enforced || !s.Persistent {
		t.Fatal(s)
	}
	g.Accepted()
	if g.Snapshot().Status != "blocked" {
		t.Fatal("lockdown alone was promoted without TUN/system-proxy verification")
	}
	if err := g.VerifyTunnel("tenebra", "172.19.0.1/30", false); err != nil {
		t.Fatal(err)
	}
	if g.Snapshot().Status != "blocked" {
		t.Fatal("unaccepted engine reported active")
	}
	g.Accepted()
	if g.Snapshot().Status != "active" {
		t.Fatal(g.Snapshot())
	}
	g.Interrupted()
	if g.Snapshot().Status != "blocked" || b.removes != 0 {
		t.Fatal("process exit released policy")
	}
	b.applyErr = errors.New("transaction failed")
	if g.Prepare() == nil || !g.Snapshot().Enforced || g.Snapshot().Status != "error" {
		t.Fatal(g.Snapshot())
	}
	b.removeErr = errors.New("cleanup failed")
	if g.Release() == nil || !g.Snapshot().Enforced {
		t.Fatal("failed release claimed off")
	}
	b.removeErr = nil
	if g.Release() != nil || g.Snapshot().Enforced || g.Snapshot().Status != "off" {
		t.Fatal(g.Snapshot())
	}
}

func TestGuardRecoveryDoesNotOpenTrafficOrInventProtection(t *testing.T) {
	b := &memoryBackend{}
	g := New(b)
	if err := g.Recover(); err != nil || len(b.applications) != 0 {
		t.Fatal("startup with no policy wrote native state")
	}
	b.present, b.complete = true, true
	if err := g.Recover(); err != nil || len(b.applications) != 1 || b.applications[0] != 0 {
		t.Fatal("recovery did not lock down", err)
	}
	b2 := &memoryBackend{applyErr: errors.New("denied")}
	g2 := New(b2)
	if g2.Prepare() == nil || g2.Snapshot().Enforced {
		t.Fatal("initial failed apply claims enforced")
	}
	if New(nil).Prepare() == nil || New(nil).Snapshot().Status != "unavailable" {
		t.Fatal("nil native adapter accepted")
	}
}
