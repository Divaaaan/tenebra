//go:build linux

package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Divaaaan/tenebra/core/buildinfo"
	"github.com/Divaaaan/tenebra/core/control"
	"github.com/Divaaaan/tenebra/core/state"
	"github.com/Divaaaan/tenebra/internal/installtx"
	"github.com/Divaaaan/tenebra/internal/securefs"
)

func TestManagedRootBootStaysHeldUntilJournalCommitAndPrivateAdmission(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("isolated root machine-policy fixture required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Keep the Unix endpoint below the kernel's path limit. This directory and
	// every source/runtime file belong only to this isolated test process.
	directory, err := os.MkdirTemp("", "tmb-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(directory); err != nil {
			t.Error(err)
		}
	})
	policy := securefs.Policy{Mode: securefs.MachinePrivate}
	root, err := securefs.OpenRoot(directory, policy, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := root.Close(); err != nil {
			t.Error(err)
		}
	})
	makeRoot := func(name string) *securefs.Root {
		t.Helper()
		child, err := root.OpenDir(name, policy, true)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := child.Close(); err != nil {
				t.Error(err)
			}
		})
		return child
	}
	data, maintenance, releases, input := makeRoot("data"), makeRoot("maintenance"), makeRoot("releases"), makeRoot("input")
	backup, err := maintenance.OpenDir("state-backups", policy, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := backup.Close(); err != nil {
			t.Error(err)
		}
	})
	tx := "it1_" + strings.Repeat("a", 32)
	launch := strings.Repeat("b", 32)
	oc := "oc1_" + strings.Repeat("c", 32)
	held, err := state.OpenHeld(ctx, data, backup)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := held.Checkpoint(ctx, tx)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := held.Migrate(ctx, tx)
	if err != nil {
		t.Fatal(err)
	}
	if err := held.Close(); err != nil {
		t.Fatal(err)
	}
	journal, err := installtx.OpenJournalFile(maintenance)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := journal.Close(); err != nil {
			t.Error(err)
		}
	})
	manifest := installtx.Manifest{Schema: 1, Kind: "tenebra-install-payload", Version: buildinfo.Version, SourceSHA: strings.Repeat("d", 40), Target: "linux-amd64"}
	for _, name := range []string{"tenebra-core", "sing-box", "geoip-ru.srs", "geosite-ru.srs", "geosite-ads.srs"} {
		body := []byte("SYNTHETIC_UNEXECUTED " + name)
		sum := sha256.Sum256(body)
		mode := uint32(0644)
		if name == "tenebra-core" || name == "sing-box" {
			mode = 0755
		}
		if outcome, err := input.ReplaceFile(name, body); err != nil || outcome != securefs.Committed {
			t.Fatal(outcome, err)
		}
		manifest.Files = append(manifest.Files, installtx.ManifestFile{Name: name, Size: int64(len(body)), SHA256: hex.EncodeToString(sum[:]), Mode: mode})
	}
	inputPath, _ := input.Path()
	stager, err := installtx.NewProtectedFS(releases, journal, installtx.SourcePolicy{Trust: "operator_dev", AcknowledgeUntrusted: true,
		OperatorPrincipal: "uid:0", HelperSHA256: strings.Repeat("e", 64), Target: "linux-amd64"})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := stager.Stage(ctx, installtx.Source{Layout: "flat", AbsolutePath: inputPath}, manifest)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := stager.PublishRuntime(ctx, payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := stager.RemoveOwnedStage(ctx, payload.StageID); err != nil {
		t.Fatal(err)
	}
	j := installtx.Journal{Schema: 1, Generation: 1, Manager: "standalone", Pending: &ref, Transaction: &installtx.Transaction{
		ID: tx, Action: "install", Phase: "schema_ready", Trust: "operator_dev", Candidate: &ref, Checkpoint: &checkpoint, SchemaReceipt: &receipt}}
	if err := journal.ReplaceJournal(ctx, j); err != nil {
		t.Fatal(err)
	}
	// The installer and core are separate processes in production. Release this
	// fixture's installer child handles before opening the core-side wrappers.
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	if err := backup.Close(); err != nil {
		t.Fatal(err)
	}
	dataPath, _ := data.Path()
	maintenancePath, _ := maintenance.Path()
	releasesPath, _ := releases.Path()
	paths := installtx.PlatformPaths{Data: dataPath, Maintenance: maintenancePath, Releases: releasesPath}
	executable := filepath.Join(releasesPath, ref.ArtifactSHA256, "tenebra-core")
	t.Setenv("TENEBRA_CONFIG_DIR", filepath.Join(directory, "poison-config"))
	t.Setenv("TENEBRA_SINGBOX", filepath.Join(directory, "poison-engine"))
	filesystem, err := openManagedFilesystemAt(ctx, paths, runAuthority{managed: true, transaction: tx, launch: launch}, executable)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := filesystem.Close(); err != nil {
			t.Error(err)
		}
	})
	daemon, err := buildDaemonWithFilesystem(&filesystem)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := daemon.Close(); err != nil {
			t.Error(err)
		}
	})
	guiListener, err := control.ListenSocket(filepath.Join(directory, "gui.sock"))
	if err != nil {
		t.Fatal(err)
	}
	guiContext, stopGUI := context.WithCancel(ctx)
	guiDone := make(chan error, 1)
	go func() { guiDone <- control.ServeListener(guiContext, daemon, guiListener) }()
	gui, err := net.Dial("unix", filepath.Join(directory, "gui.sock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { gui.Close(); stopGUI(); guiListener.Close(); <-guiDone })
	guiReader := bufio.NewReader(gui)
	checkGUI := func(id int) {
		t.Helper()
		gui.SetDeadline(time.Now().Add(2 * time.Second))
		if _, err := fmt.Fprintf(gui, "{\"id\":%d,\"cmd\":\"status\"}\n", id); err != nil {
			t.Fatal("business session was displaced", err)
		}
		for {
			line, err := guiReader.ReadBytes('\n')
			if err != nil {
				t.Fatal("business session was displaced", err)
			}
			var frame struct {
				ID int
				Ok bool
			}
			if json.Unmarshal(line, &frame) != nil {
				t.Fatal("malformed business frame")
			}
			if frame.ID == id {
				if !frame.Ok {
					t.Fatal("status refused")
				}
				return
			}
		}
	}
	checkGUI(101)
	if filesystem.Development || !errors.Is(daemon.CanonicalReady(), state.ErrMaintenancePending) {
		t.Fatal("root boot inherited development admission")
	}
	var starts atomic.Int32
	filesystem.managed.startup = func(_ *control.Daemon, allowAutoconnect bool) {
		if allowAutoconnect {
			t.Error("first activation enabled saved autoconnect")
		}
		starts.Add(1)
	}
	closePrivate, err := startManagedPrivate(ctx, daemon, &filesystem)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closePrivate)
	probe, err := installtx.NewReadyProbe(filesystem.managed.readyRoot, tx)
	if err != nil {
		t.Fatal(err)
	}
	token, err := installtx.ProcessStartToken(uint32(os.Getpid()))
	if err != nil {
		t.Fatal(err)
	}
	coreSHA := ""
	for _, file := range manifest.Files {
		if file.Name == "tenebra-core" {
			coreSHA = file.SHA256
		}
	}
	expect := installtx.ReadyExpectation{Schema: 1, StoreSchema: 2, TxID: tx, DaemonVersion: buildinfo.Version, LaunchID: launch, CoreSHA256: coreSHA,
		Transport: "unix", BusinessAdmission: "closed", StoreRevision: receipt.Revision, Process: installtx.ProcessIdentity{PID: uint32(os.Getpid()), StartToken: token, LaunchID: launch}}
	maintenanceProbe, err := installtx.NewMaintenanceReadyProbe(filesystem.managed.readyRoot)
	if err != nil {
		t.Fatal(err)
	}
	ready, err := installtx.VerifyReady(ctx, maintenanceProbe, expect)
	if err != nil {
		t.Fatal(err)
	}
	checkGUI(102)
	if _, err := installtx.AdmitPrivate(ctx, probe, expect.Process, tx, oc, ready); err == nil || starts.Load() != 0 {
		t.Fatal("schema-ready journal admitted writer")
	}
	j.Generation++
	j.Active = &ref
	j.Pending = nil
	j.Transaction.Phase = "committed_closed"
	j.Transaction.OperationalCommitID = oc
	if err := filesystem.managed.journal.ReplaceJournal(ctx, j); err != nil {
		t.Fatal(err)
	}
	if _, err := installtx.AdmitPrivate(ctx, probe, expect.Process, tx, oc, ready); err != nil {
		t.Fatal(err)
	}
	if daemon.CanonicalReady() != nil || starts.Load() != 1 {
		t.Fatal("exact durable operational admission did not open writer")
	}
	checkGUI(103)
	nextTx := "it1_" + strings.Repeat("e", 32)
	if _, err := installtx.FencePrivate(ctx, probe, expect.Process, nextTx, ready); err == nil || daemon.BusinessReady() != nil {
		t.Fatal("uncommitted new transaction fenced live daemon", err)
	}
	j.Generation++
	j.Transaction = &installtx.Transaction{ID: nextTx, Action: "remove", Phase: "fencing", Trust: "release", PriorActive: &ref}
	if err := filesystem.managed.journal.ReplaceJournal(ctx, j); err != nil {
		t.Fatal(err)
	}
	fenced, err := installtx.FencePrivate(ctx, probe, expect.Process, nextTx, ready)
	if err != nil || fenced.Status != "fenced" || fenced.TxID != nextTx || !errors.Is(daemon.BusinessReady(), state.ErrMaintenancePending) {
		t.Fatal("actual old-process fence failed", fenced, err)
	}
	checkGUI(104)
	if _, err := installtx.AdmitPrivate(ctx, probe, expect.Process, tx, oc, ready); err == nil {
		t.Fatal("obsolete operational ID reopened fenced process")
	}
	checkGUI(105)
	if _, err := os.Stat(filepath.Join(directory, "poison-config")); !os.IsNotExist(err) {
		t.Fatal("root boot consumed environment path", err)
	}
}
