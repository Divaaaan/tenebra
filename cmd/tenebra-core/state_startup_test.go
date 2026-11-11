package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/Divaaaan/tenebra/core/control"
	"github.com/Divaaaan/tenebra/core/state"
	"github.com/Divaaaan/tenebra/internal/securefs"
)

func canonicalStartupFixture(t *testing.T, development bool) (daemonFilesystem, *securefs.Root) {
	t.Helper()
	if base := os.Getenv("TENEBRA_SECUREFS_TEST_ROOT"); base != "" {
		t.Setenv("GOTMPDIR", filepath.Clean(base))
	} else if runtime.GOOS == "windows" {
		t.Fatal("set explicit protected TENEBRA_SECUREFS_TEST_ROOT")
	}
	policy := securefs.Policy{Mode: securefs.SameAccountPrivate}
	parent, err := securefs.OpenRoot(filepath.Join(t.TempDir(), "private"), policy, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := parent.Close(); err != nil {
			t.Error(err)
		}
	})
	data, err := parent.OpenDir("data", policy, true)
	if err != nil {
		t.Fatal(err)
	}
	private, err := parent.OpenDir("maintenance", policy, true)
	if err != nil {
		t.Fatal(err)
	}
	return daemonFilesystem{State: data, Private: private, Development: development, privatePolicy: policy, closers: []io.Closer{data, private}}, parent
}
func TestCanonicalStartupOwnsSoleWriterAndRetainsItUntilFilesystemClose(t *testing.T) {
	filesystem, parent := canonicalStartupFixture(t, true)
	legacy := []byte(`{"version":2,"kill_switch":true,"preset_voice_direct":false}`)
	if _, err := filesystem.State.ReplaceFile("profiles.json", []byte("[]")); err != nil {
		t.Fatal(err)
	}
	if _, err := filesystem.State.ReplaceFile("settings.json", legacy); err != nil {
		t.Fatal(err)
	}
	bootstrap := daemonBootstrap{Open: func(context.Context) (daemonFilesystem, error) { return filesystem, nil }, Build: buildDaemonWithFilesystem}
	daemon, opened, err := bootstrap.Prepare(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := opened.State.LockWriter(context.Background()); !errors.Is(err, securefs.ErrBusy) {
		t.Fatal("canonical store did not own sole writer", err)
	}
	if response := daemon.Handle(context.Background(), control.Request{Cmd: control.CmdSetTLSFragment, On: true}); !response.Ok {
		t.Fatal(response.Error)
	}
	raw, err := opened.State.ReadFile("profiles.json", 64<<20)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot state.Snapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Validate() != nil || !snapshot.Preferences.KillSwitch || !snapshot.Preferences.TLSFragment || snapshot.Revision != 2 {
		t.Fatal("startup lost legacy intent or used a second preference file")
	}
	stale, err := opened.State.ReadFile("settings.json", 1<<20)
	if err != nil || string(stale) != string(legacy) {
		t.Fatal("legacy writer remained active", err)
	}
	if err := opened.Close(); err != nil {
		t.Fatal("repository lifetime was not retained by returned filesystem", err)
	}
	data, err := parent.OpenDir("data", filesystem.privatePolicy, false)
	if err != nil {
		t.Fatal(err)
	}
	defer data.Close()
	private, err := parent.OpenDir("maintenance", filesystem.privatePolicy, false)
	if err != nil {
		t.Fatal(err)
	}
	defer private.Close()
	if _, err := data.ReplaceFile("settings.json", []byte("{CORRUPT_OBSOLETE_SIDE_FILE")); err != nil {
		t.Fatal(err)
	}
	second := daemonFilesystem{State: data, Private: private, Development: true, privatePolicy: filesystem.privatePolicy}
	d, err := buildDaemonWithFilesystem(&second)
	if err != nil {
		t.Fatal("schema2 reopened obsolete settings as business input", err)
	}
	defer second.Close()
	if response := d.Handle(context.Background(), control.Request{Cmd: control.CmdStatus}); !response.Ok {
		t.Fatal(response)
	}
	raw, err = data.ReadFile("profiles.json", 64<<20)
	if err != nil {
		t.Fatal(err)
	}
	var reopened state.Snapshot
	if err := json.Unmarshal(raw, &reopened); err != nil {
		t.Fatal(err)
	}
	if reopened.MigrationID != snapshot.MigrationID || reopened.Revision != snapshot.Revision || !reopened.Preferences.TLSFragment {
		t.Fatal("restart remigrated or reverted canonical intent")
	}
}
func TestCanonicalMachineCompositionDoesNotAdmitBeforeOperationalOwner(t *testing.T) {
	f, _ := canonicalStartupFixture(t, false)
	defer func() {
		if err := f.Close(); err != nil {
			t.Error(err)
		}
	}()
	d, err := buildDaemonWithFilesystem(&f)
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(d.CanonicalReady(), state.ErrMaintenancePending) {
		t.Fatal("machine composition admitted without durable operational owner")
	}
	if response := d.Handle(context.Background(), control.Request{Cmd: control.CmdSetTLSFragment, On: true}); response.Ok || response.Error != "maintenance_pending" {
		t.Fatal(response)
	}
}
