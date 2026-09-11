package control

import (
	"errors"
	"testing"
)

type memoryUserProxy struct {
	settings   userProxySettings
	lease      *userProxyLease
	writes     int
	failWrite  int
	failSave   bool
	failDelete bool
}

func (m *memoryUserProxy) Read() (userProxySettings, error) { return m.settings, nil }
func (m *memoryUserProxy) Write(s userProxySettings) error {
	m.writes++
	if m.writes == m.failWrite {
		m.settings.Server = s.Server
		return errors.New("partial write")
	}
	m.settings = s
	return nil
}
func (m *memoryUserProxy) Load() (*userProxyLease, error) { return m.lease, nil }
func (m *memoryUserProxy) Save(l userProxyLease) error {
	if m.failSave {
		return errors.New("snapshot unavailable")
	}
	m.lease = &l
	return nil
}
func (m *memoryUserProxy) Delete() error {
	if m.failDelete {
		return errors.New("delete failed")
	}
	m.lease = nil
	return nil
}

func TestUserProxyRestoresCorporateSettingsAndPAC(t *testing.T) {
	before := userProxySettings{Flags: 15, Server: "corp.example:8080", Bypass: "intranet;*.internal", PAC: "https://config.example/proxy.pac"}
	m := &memoryUserProxy{settings: before}
	if err := applyUserProxy(m, "127.0.0.1:2080"); err != nil {
		t.Fatal(err)
	}
	if m.settings.Flags != 3 || m.settings.Server != "127.0.0.1:2080" || m.settings.PAC != "" {
		t.Fatalf("proxy not applied: %+v", m.settings)
	}
	if m.lease == nil || m.lease.Before != before {
		t.Fatal("original settings not retained")
	}
	if err := restoreUserProxy(m); err != nil {
		t.Fatal(err)
	}
	if m.settings != before || m.lease != nil {
		t.Fatal("original proxy/PAC not fully restored")
	}
}

func TestUserProxySnapshotFailureNeverMutatesSettings(t *testing.T) {
	m := &memoryUserProxy{failSave: true, settings: userProxySettings{Flags: 9}}
	if err := applyUserProxy(m, "127.0.0.1:2080"); err == nil {
		t.Fatal("snapshot failure accepted")
	}
	if m.writes != 0 {
		t.Fatal("changed settings before durable rollback snapshot")
	}
}

func TestUserProxyPartialApplyAndCleanupRetry(t *testing.T) {
	before := userProxySettings{Flags: 9, Server: "old:80", PAC: "https://config.example/pac"}
	m := &memoryUserProxy{settings: before, failWrite: 1, failDelete: true}
	if err := applyUserProxy(m, "127.0.0.1:2080"); err == nil {
		t.Fatal("partial apply accepted")
	}
	if m.settings != before || m.lease == nil {
		t.Fatal("partial change not restored or retry ownership lost")
	}
	m.failDelete = false
	if err := restoreUserProxy(m); err != nil {
		t.Fatal(err)
	}
	if m.lease != nil || m.settings != before {
		t.Fatal("retry did not finish restore")
	}
}

func TestUserProxyRepeatedApplyDoesNotOverwriteOriginalSnapshot(t *testing.T) {
	before := userProxySettings{Flags: 1}
	m := &memoryUserProxy{settings: before}
	if err := applyUserProxy(m, "127.0.0.1:2080"); err != nil {
		t.Fatal(err)
	}
	if err := applyUserProxy(m, "127.0.0.1:2080"); err != nil {
		t.Fatal(err)
	}
	if m.lease.Before != before || m.writes != 1 {
		t.Fatal("idempotent apply lost original state")
	}
	if err := applyUserProxy(m, "127.0.0.1:2081"); err != nil {
		t.Fatal(err)
	}
	if m.lease.Before != before || m.settings.Server != "127.0.0.1:2081" {
		t.Fatal("port change lost original state")
	}
}

func TestUserProxyRestorePreservesLaterUserChange(t *testing.T) {
	m := &memoryUserProxy{settings: userProxySettings{Flags: 1}}
	if err := applyUserProxy(m, "127.0.0.1:2080"); err != nil {
		t.Fatal(err)
	}
	changed := userProxySettings{Flags: 7, Server: "new-corporate:8888", Bypass: "work", PAC: "https://work/pac"}
	m.settings = changed
	if err := restoreUserProxy(m); err != nil {
		t.Fatal(err)
	}
	if m.settings != changed || m.lease != nil {
		t.Fatal("cleanup overwrote newer external settings")
	}
}

func TestUserProxyRejectsNonLoopbackAndInvalidTargets(t *testing.T) {
	for _, target := range []string{"192.0.2.1:2080", "127.0.0.1:0", "127.0.0.1:65536", "localhost:2080", "127.0.0.1:2080;https=evil:80"} {
		m := &memoryUserProxy{}
		if err := applyUserProxy(m, target); err == nil {
			t.Errorf("accepted %q", target)
		}
		if m.writes != 0 || m.lease != nil {
			t.Fatal("invalid target changed settings")
		}
	}
}
