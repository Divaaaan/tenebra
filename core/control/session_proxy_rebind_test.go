package control

import (
	"errors"
	"testing"
)

type logonProxySessions struct {
	current      proxyUser
	hasLease     bool
	restoreUsers []proxyUser
}

func (m *logonProxySessions) Current() (proxyUser, error)        { return m.current, nil }
func (m *logonProxySessions) Read(proxyUser) (proxyState, error) { return proxyState{}, nil }
func (m *logonProxySessions) HasLease(proxyUser) (bool, error)   { return m.hasLease, nil }
func (m *logonProxySessions) Run(u proxyUser, action, _ string) error {
	if u != m.current {
		return errors.New("WTSQueryUserToken: prior session is gone")
	}
	if action == "restore" {
		m.restoreUsers = append(m.restoreUsers, u)
	}
	m.hasLease = action == "apply"
	return nil
}

func TestUserProxyReconcileRebindsOnlySameSIDNewSession(t *testing.T) {
	for _, sameUser := range []bool{true, false} {
		t.Run(map[bool]string{true: "same SID", false: "different SID"}[sameUser], func(t *testing.T) {
			original := proxyUser{SID: "S-1-5-21-1000", Session: 1}
			m := &logonProxySessions{current: original}
			p := &sessionSystemProxy{ops: m}
			if err := p.Enable("127.0.0.1:2080"); err != nil {
				t.Fatal(err)
			}
			m.current.Session = 2
			if !sameUser {
				m.current.SID = "S-1-5-21-2000"
			}
			found, err := p.Reconcile()
			if !found {
				t.Fatal("retained cleanup was lost")
			}
			if sameUser {
				if err != nil || p.owner != nil || m.hasLease || len(m.restoreUsers) != 1 || m.restoreUsers[0] != m.current {
					t.Fatalf("same user could not recover at new logon: owner=%+v error=%v", p.owner, err)
				}
			} else if err == nil || p.owner == nil || *p.owner != original || !m.hasLease || len(m.restoreUsers) != 0 {
				t.Fatal("cleanup was transferred to another SID")
			}
		})
	}
}
