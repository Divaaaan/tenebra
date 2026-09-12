package control

import (
	"errors"
	"testing"
)

type fakeProxySessions struct {
	current proxyUser
	fail    bool
	has     bool
	users   []proxyUser
	actions []string
}

func (f *fakeProxySessions) Current() (proxyUser, error) { return f.current, nil }
func (f *fakeProxySessions) Run(u proxyUser, a, _ string) error {
	f.users = append(f.users, u)
	f.actions = append(f.actions, a)
	if f.fail {
		return errors.New("session temporarily unavailable")
	}
	return nil
}
func (f *fakeProxySessions) Read(proxyUser) (proxyState, error) {
	return proxyState{Enabled: true, Server: "127.0.0.1:2080"}, nil
}
func (f *fakeProxySessions) HasLease(proxyUser) (bool, error) { return f.has, nil }

func TestUserProxyCleanupStaysWithOriginalSession(t *testing.T) {
	a := proxyUser{SID: "S-1-5-21-100", Session: 1}
	b := proxyUser{SID: "S-1-5-21-200", Session: 2}
	f := &fakeProxySessions{current: a}
	p := &sessionSystemProxy{ops: f}
	if err := p.Enable("127.0.0.1:2080"); err != nil {
		t.Fatal(err)
	}
	f.current = b
	f.fail = true
	if err := p.Disable(); err == nil {
		t.Fatal("cleanup failure lost")
	}
	f.fail = false
	if err := p.Disable(); err != nil {
		t.Fatal(err)
	}
	for _, u := range f.users {
		if u != a {
			t.Fatalf("cleanup retargeted to another user: %+v", u)
		}
	}
	if err := p.Enable("127.0.0.1:2081"); err != nil {
		t.Fatal(err)
	}
	if f.users[len(f.users)-1] != b {
		t.Fatal("new apply did not select the new user")
	}
}

func TestUserProxyFailedApplyKeepsOriginalOwner(t *testing.T) {
	a := proxyUser{SID: "first", Session: 1}
	f := &fakeProxySessions{current: a, fail: true}
	p := &sessionSystemProxy{ops: f}
	if p.Enable("127.0.0.1:2080") == nil {
		t.Fatal("apply should fail")
	}
	f.current = proxyUser{SID: "second", Session: 1}
	f.fail = false
	if err := p.Disable(); err != nil {
		t.Fatal(err)
	}
	if f.users[1] != a {
		t.Fatal("reused session id replaced cleanup owner")
	}
}

func TestUserProxyReconcileRequiresLeaseAndRetriesFailure(t *testing.T) {
	f := &fakeProxySessions{current: proxyUser{SID: "user", Session: 1}}
	p := &sessionSystemProxy{ops: f}
	if changed, err := p.Reconcile(); changed || err != nil || len(f.users) != 0 {
		t.Fatal("matching port without ownership was changed")
	}
	f.has = true
	f.fail = true
	if changed, err := p.Reconcile(); !changed || err == nil {
		t.Fatal("failed stale lease cleanup not surfaced")
	}
	f.current = proxyUser{SID: "other", Session: 2}
	f.fail = false
	if changed, err := p.Reconcile(); !changed || err != nil {
		t.Fatal("cleanup did not retry")
	}
	if f.users[0] != f.users[1] {
		t.Fatal("reconcile retry changed target user")
	}
}

func TestUserProxyStartupFailureRetainsDaemonCleanup(t *testing.T) {
	d, _ := bareDaemonWithProxy(t)
	f := &fakeProxySessions{current: proxyUser{SID: "user", Session: 1}, has: true, fail: true}
	d.proxy = &sessionSystemProxy{ops: f}
	if cleared, err := d.ReconcileSystemProxyAtStartup(); cleared || err == nil {
		t.Fatal("failed startup restore claimed success")
	}
	if !d.proxyArmed {
		t.Fatal("daemon forgot startup cleanup obligation")
	}
	f.fail = false
	if err := d.disarmSystemProxy(); err != nil {
		t.Fatal(err)
	}
	if d.proxyArmed {
		t.Fatal("cleanup obligation survived confirmed restore")
	}
}
