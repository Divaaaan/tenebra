//go:build windows

package main

import (
	"context"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Divaaaan/tenebra/core/control"
	"github.com/Divaaaan/tenebra/internal/securefs"
	"golang.org/x/sys/windows"
)

var fixturePolicy = securefs.Policy{Mode: securefs.SameAccountPrivate}

func serviceFixtureAnchor(t *testing.T) *securefs.Root {
	t.Helper()
	base := os.Getenv("TENEBRA_SECUREFS_TEST_ROOT")
	if base == "" || !filepath.IsAbs(base) {
		t.Fatal("explicit protected native fixture root is required")
	}
	t.Setenv("GOTMPDIR", base)
	root, err := securefs.OpenRoot(filepath.Join(t.TempDir(), "machine"), fixturePolicy, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := root.Close(); err != nil {
			t.Error(err)
		}
	})
	return root
}

func seedServiceState(t *testing.T, anchor *securefs.Root) *securefs.Root {
	t.Helper()
	container, err := anchor.OpenDir("Tenebra", fixturePolicy, true)
	if err != nil {
		t.Fatal(err)
	}
	state, err := container.OpenDir("data", fixturePolicy, true)
	if err != nil {
		t.Fatal(err)
	}
	// Close the parent only after its final child is released.
	t.Cleanup(func() { state.Close(); container.Close() })
	return state
}

func grantForeignWrite(t *testing.T, root *securefs.Root, relative string) string {
	t.Helper()
	path, err := root.Path()
	if err != nil {
		t.Fatal(err)
	}
	actor, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;FA;;;" + actor.User.Sid.String() + ")(A;OICI;FA;;;WD)")
	if err != nil {
		t.Fatal(err)
	}
	acl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(path, relative)
	if err := windows.SetNamedSecurityInfo(target, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
		t.Fatal(err)
	}
	return target
}

func prepareFixture(anchor *securefs.Root, calls *int) (*control.Daemon, daemonFilesystem, error) {
	bootstrap := daemonBootstrap{
		Open: func(ctx context.Context) (daemonFilesystem, error) {
			return openMachineLayout(ctx, anchor, fixturePolicy, fixturePolicy)
		},
		Build: func(*daemonFilesystem) (*control.Daemon, error) {
			*calls++
			// No real loader, runner, OS policy or process is used by fixtures.
			return &control.Daemon{}, nil
		},
	}
	return bootstrap.Prepare(context.Background())
}

func TestServiceFreshAbsentStateCanInitialize(t *testing.T) {
	anchor := serviceFixtureAnchor(t)
	calls := 0
	_, filesystem, err := prepareFixture(anchor, &calls)
	if err != nil {
		t.Fatal(err)
	}
	defer filesystem.Close()
	if calls != 1 || filesystem.State == nil || filesystem.Private == nil {
		t.Fatalf("fresh install was not gated before build: calls=%d", calls)
	}
	if _, err := filesystem.State.ReadFile("profiles.json", 128); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("invented existing profiles: %v", err)
	}
}

func TestServiceTrustedProfilesSettingsLastGoodPreserved(t *testing.T) {
	anchor := serviceFixtureAnchor(t)
	state := seedServiceState(t, anchor)
	values := map[string]string{"profiles.json": `{"legacy":"KEEP_PROFILE"}`, "settings.json": `{"apps":["App[1].exe"]}`, "lastgood.json": `{"last":"KEEP_NODE"}`}
	for name, data := range values {
		if _, err := state.ReplaceFile(name, []byte(data)); err != nil {
			t.Fatal(err)
		}
	}
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}
	calls := 0
	_, filesystem, err := prepareFixture(anchor, &calls)
	if err != nil {
		t.Fatal(err)
	}
	defer filesystem.Close()
	for name, value := range values {
		data, err := filesystem.State.ReadFile(name, 1024)
		if err != nil || string(data) != value {
			t.Fatalf("trusted %s was reset: %q %v", name, data, err)
		}
	}
}

func TestServiceMixedTrustedAndForeignStateRequiresRecovery(t *testing.T) {
	anchor := serviceFixtureAnchor(t)
	state := seedServiceState(t, anchor)
	if _, err := state.ReplaceFile("profiles.json", []byte("TRUSTED_BYTES_KEEP")); err != nil {
		t.Fatal(err)
	}
	if _, err := state.ReplaceFile("settings.json", []byte("INERT_FOREIGN_JSON")); err != nil {
		t.Fatal(err)
	}
	grantForeignWrite(t, state, "settings.json")
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}
	calls := 0
	_, filesystem, err := prepareFixture(anchor, &calls)
	if !errors.Is(err, errRecoveryRequired) || calls != 0 {
		filesystem.Close()
		t.Fatalf("foreign bytes reached consumers: calls=%d err=%v", calls, err)
	}
	container, err := anchor.OpenDir("Tenebra", fixturePolicy, false)
	if err != nil {
		t.Fatal(err)
	}
	defer container.Close()
	state, err = container.OpenDir("data", fixturePolicy, false)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	if data, err := state.ReadFile("profiles.json", 128); err != nil || string(data) != "TRUSTED_BYTES_KEEP" {
		t.Fatalf("trusted sibling lost: %q %v", data, err)
	}
	if _, err := state.ReadFile("settings.json", 128); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("foreign setting still canonical: %v", err)
	}
}

func TestServiceStartupRejectsPlantedJSONAndBatchBeforeAnyConsumer(t *testing.T) {
	anchor := serviceFixtureAnchor(t)
	state := seedServiceState(t, anchor)
	for name, content := range map[string]string{"profiles.json": `{"inert":true}`, "launcher.bat": "@rem INERT: NEVER EXECUTE\r\n"} {
		if _, err := state.ReplaceFile(name, []byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	grantForeignWrite(t, state, ".")
	state.Close()
	calls := 0
	_, filesystem, err := prepareFixture(anchor, &calls)
	if !errors.Is(err, errRecoveryRequired) || calls != 0 {
		filesystem.Close()
		t.Fatalf("planted data reached loader/autoconnect/updater/launcher: calls=%d error=%v", calls, err)
	}
	for _, cleanCreate := range []bool{false, true} {
		if cleanCreate {
			container, err := anchor.OpenDir("Tenebra", fixturePolicy, false)
			if err != nil {
				t.Fatal(err)
			}
			fresh, err := container.OpenDir("data", fixturePolicy, true)
			if err != nil {
				t.Fatal(err)
			}
			fresh.Close()
			container.Close()
		}
		_, filesystem, err = prepareFixture(anchor, &calls)
		if !errors.Is(err, errRecoveryRequired) || calls != 0 {
			filesystem.Close()
			t.Fatalf("restart cleanCreate=%v cleared pending recovery: calls=%d error=%v", cleanCreate, calls, err)
		}
	}
}

func TestServiceSCMUsesPrepareBeforeLogBuildAndBackground(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "service_windows.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var prepare, logOpen, build, background token.Pos
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fn := call.Fun.(type) {
		case *ast.SelectorExpr:
			if fn.Sel.Name == "Prepare" {
				prepare = call.Pos()
			}
		case *ast.Ident:
			switch fn.Name {
			case "openServiceLog":
				logOpen = call.Pos()
			case "buildDaemon", "configureServicePaths", "secureDataDir":
				build = call.Pos()
			case "startBackgroundJobs":
				background = call.Pos()
			}
		}
		return true
	})
	if prepare == 0 || logOpen < prepare || background < prepare || build != 0 {
		t.Fatalf("SCM pre-read order: prepare=%d log=%d background=%d legacy consumer=%d", prepare, logOpen, background, build)
	}
	data, err := os.ReadFile("service_windows.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "SetNamedSecurityInfo") || strings.Contains(string(data), `Getenv("ProgramData")`) {
		t.Fatal("SCM still changes existing ownership/ACL or trusts the ProgramData environment")
	}
}

func TestServicePrivateAnchorAndPlantedLogFailBeforeBuild(t *testing.T) {
	for _, target := range []string{"anchor", "log"} {
		t.Run(target, func(t *testing.T) {
			anchor := serviceFixtureAnchor(t)
			private, err := anchor.OpenDir("Tenebra-Private", fixturePolicy, true)
			if err != nil {
				t.Fatal(err)
			}
			if target == "anchor" {
				grantForeignWrite(t, private, ".")
			} else {
				logs, err := private.OpenDir("logs", fixturePolicy, true)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := logs.ReplaceFile("service.log", []byte("INERT_FOREIGN_LOG")); err != nil {
					t.Fatal(err)
				}
				grantForeignWrite(t, logs, "service.log")
				logs.Close()
			}
			private.Close()
			calls := 0
			_, filesystem, err := prepareFixture(anchor, &calls)
			if err == nil || calls != 0 {
				filesystem.Close()
				t.Fatalf("unsafe private %s reached consumers: calls=%d error=%v", target, calls, err)
			}
		})
	}
}

func TestServiceMissingOrIncompleteIndexedReceiptNeverInitializesState(t *testing.T) {
	for _, record := range []string{"missing", "pending_detach", "detached_pending_copy", "recovery_required", "malformed"} {
		t.Run(record, func(t *testing.T) {
			anchor := serviceFixtureAnchor(t)
			private, err := anchor.OpenDir("Tenebra-Private", fixturePolicy, true)
			if err != nil {
				t.Fatal(err)
			}
			vault, err := private.OpenDir("forensics", fixturePolicy, true)
			if err != nil {
				t.Fatal(err)
			}
			id := "q_11111111111111111111111111111111"
			index, _ := json.Marshal(map[string]any{"version": 1, "ids": []string{id}})
			if _, err := vault.ReplaceFile("quarantine-index.json", index); err != nil {
				t.Fatal(err)
			}
			if record != "missing" {
				data, _ := json.Marshal(map[string]any{"version": 1, "id": id, "state": record, "acknowledged": false})
				if record == "malformed" {
					data = []byte("{INCOMPLETE_JSON")
				}
				if _, err := vault.ReplaceFile("quarantine-"+id+".json", data); err != nil {
					t.Fatal(err)
				}
			}
			vault.Close()
			private.Close()
			calls := 0
			_, filesystem, err := prepareFixture(anchor, &calls)
			if !errors.Is(err, errRecoveryRequired) || calls != 0 {
				filesystem.Close()
				t.Fatalf("receipt %s failed open: calls=%d error=%v", record, calls, err)
			}
			if container, err := anchor.OpenDir("Tenebra", fixturePolicy, false); !errors.Is(err, fs.ErrNotExist) {
				if container != nil {
					container.Close()
				}
				t.Fatalf("pending recovery was treated as fresh state: %v", err)
			}
		})
	}
}

func TestServicePrivateLogBoundedReopenAndRootLifetime(t *testing.T) {
	anchor := serviceFixtureAnchor(t)
	calls := 0
	_, filesystem, err := prepareFixture(anchor, &calls)
	if err != nil {
		t.Fatal(err)
	}
	defer filesystem.Close()
	writer, err := openServiceLog(filesystem)
	if err != nil {
		t.Fatal(err)
	}
	if err := filesystem.Private.Close(); !errors.Is(err, securefs.ErrBusy) {
		t.Fatalf("live logger did not retain private root: %v", err)
	}
	if _, err := writer.Write([]byte(strings.Repeat("x", serviceLogLimit+10) + "\nFINAL_DIAGNOSTIC\n")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	writer, err = openServiceLog(filesystem)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if len(writer.data) > serviceLogLimit || !strings.Contains(strings.Join(writer.Tail(5), "\n"), "FINAL_DIAGNOSTIC") {
		t.Fatal("private diagnostic tail was lost or unbounded")
	}
}
