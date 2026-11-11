//go:build linux

package installtx

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Scripted native OS boundary only. These cases verify controller decisions,
// fixed arguments and failure preservation; they never invoke real systemctl,
// install/enable a host unit or substitute for native whole-job acceptance.
type scriptedSystemd struct {
	units                          map[string][]byte
	state                          systemdJobState
	commands                       []string
	publications                   int
	publishFailure, commandFailure error
	keepTasks                      bool
}

func (s *scriptedSystemd) ReadUnit(_ context.Context, path string) ([]byte, error) {
	body, ok := s.units[path]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return append([]byte{}, body...), nil
}
func (s *scriptedSystemd) PublishUnit(_ context.Context, path string, body []byte, previous string) error {
	if s.publishFailure != nil {
		return s.publishFailure
	}
	old, exists := s.units[path]
	if previous == "" {
		if exists {
			return ErrConflict
		}
	} else {
		sum := sha256.Sum256(old)
		if !exists || hex.EncodeToString(sum[:]) != previous {
			return ErrConflict
		}
	}
	s.units[path] = append([]byte{}, body...)
	s.publications++
	return nil
}
func (s *scriptedSystemd) Query(context.Context) (systemdJobState, error) {
	copy := systemdJobState{}
	for k, v := range s.state {
		copy[k] = v
	}
	return copy, nil
}
func (s *scriptedSystemd) Command(_ context.Context, action string) error {
	s.commands = append(s.commands, action)
	if s.commandFailure != nil {
		return s.commandFailure
	}
	switch action {
	case "daemon-reload":
		for path := range s.units {
			s.state["FragmentPath"] = path
			s.state["LoadState"] = "loaded"
			s.state["UnitFileState"] = "disabled"
		}
	case "enable":
		s.state["UnitFileState"] = "enabled"
	case "disable":
		s.state["UnitFileState"] = "disabled"
	case "start":
		s.state["ActiveState"] = "active"
		s.state["SubState"] = "running"
		s.state["MainPID"] = "123"
		s.state["ControlGroup"] = "/system.slice/tenebra.service"
		s.state["TasksCurrent"] = "1"
	case "stop":
		s.state["ActiveState"] = "inactive"
		s.state["SubState"] = "dead"
		s.state["MainPID"] = "0"
		s.state["ControlPID"] = "0"
		if !s.keepTasks {
			s.state["ControlGroup"] = ""
			s.state["TasksCurrent"] = "[not set]"
		}
	default:
		return ErrProtocol
	}
	return nil
}
func baseSystemdState() systemdJobState {
	s := systemdJobState{}
	for _, key := range strings.Split(systemdPropertyNames, ",") {
		s[key] = ""
	}
	for k, v := range map[string]string{"Id": "tenebra.service", "LoadState": "not-found", "Transient": "no", "NeedDaemonReload": "no", "ActiveState": "inactive", "SubState": "dead",
		"MainPID": "0", "ControlPID": "0", "TasksCurrent": "[not set]", "KillMode": "control-group", "SendSIGKILL": "yes", "Delegate": "no"} {
		s[k] = v
	}
	return s
}
func systemdFixture(t *testing.T) (*LinuxServiceHostJob, *scriptedSystemd, *installDisk) {
	t.Helper()
	d := installerDisk()
	d.journal.Active = nil
	d.journal.Pending = d.journal.Transaction.Candidate
	d.journal.Transaction.Phase = "schema_ready"
	d.journal.Transaction.OperationalCommitID = ""
	if err := d.journal.Validate(); err != nil {
		t.Fatal(err)
	}
	boundary := &scriptedSystemd{units: map[string][]byte{}, state: baseSystemdState()}
	return &LinuxServiceHostJob{journal: fakeInstallFS{d}, boundary: boundary}, boundary, d
}
func loadSystemdFixture(b *scriptedSystemd, path string) {
	b.units[path] = []byte(linuxServiceHostUnit)
	b.state["FragmentPath"] = path
	b.state["LoadState"] = "loaded"
	b.state["UnitFileState"] = "enabled"
}

func TestSystemdFixedHostStartIsOnlyAnOSAck(t *testing.T) {
	job, b, _ := systemdFixture(t)
	if err := job.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(b.commands, []string{"daemon-reload", "enable", "start"}) || b.publications != 1 {
		t.Fatal("unexpected native actions", b.commands, b.publications)
	}
	if !bytes.Equal(b.units["/etc/systemd/system/tenebra.service"], []byte(linuxServiceHostUnit)) || !strings.Contains(linuxServiceHostUnit, "tenebra-helper service-host\n") || strings.Contains(linuxServiceHostUnit, "TENEBRA_") {
		t.Fatal("unit does not route through fixed managed host")
	}
	if _, err := PlanServiceHost(installerDisk().journal, nil); err != nil {
		t.Fatal(err)
	}
	// No protected process/ready/admitted record exists at this OS boundary.
	// P0 manager still has to obtain the actual private readiness challenge.
	if err := job.Start(context.Background()); !errors.Is(err, ErrRecoveryRequired) || b.publications != 1 || len(b.commands) != 3 {
		t.Fatal("second start adopted active process or published twice", err, b.commands)
	}
}

func TestSystemdStopChecksWholeJobAndRemoveDisables(t *testing.T) {
	job, b, d := systemdFixture(t)
	loadSystemdFixture(b, "/etc/systemd/system/tenebra.service")
	b.state["ActiveState"] = "active"
	b.state["SubState"] = "running"
	b.state["MainPID"] = "123"
	b.state["ControlGroup"] = "/system.slice/tenebra.service"
	b.state["TasksCurrent"] = "3"
	ref := *d.journal.Transaction.Candidate
	d.journal.Active = &ref
	d.journal.Pending = nil
	d.journal.Transaction = &Transaction{ID: "it1_" + strings.Repeat("a", 32), Action: "remove", Phase: "fencing", PriorActive: &ref, Trust: "release"}
	if err := job.StopAndWait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(b.commands, []string{"stop", "disable"}) || !b.state.stopped() || b.state["UnitFileState"] != "disabled" {
		t.Fatal("remove did not stop whole job and disable", b.commands, b.state)
	}
	if err := job.StopAndWait(context.Background()); err != nil || len(b.commands) != 2 {
		t.Fatal("idempotent remove changed OS state", err, b.commands)
	}
}

func TestSystemdZeroMainPIDWithRemainingTasksNeverConfirmsStop(t *testing.T) {
	job, b, _ := systemdFixture(t)
	loadSystemdFixture(b, "/etc/systemd/system/tenebra.service")
	b.state["ActiveState"] = "inactive"
	b.state["SubState"] = "dead"
	b.state["MainPID"] = "0"
	b.state["ControlGroup"] = "/system.slice/tenebra.service"
	b.state["TasksCurrent"] = "1"
	b.keepTasks = true
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Millisecond)
	defer cancel()
	if err := job.StopAndWait(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("PID0 was accepted while a descendant remained", err)
	}
	if !reflect.DeepEqual(b.commands, []string{"stop"}) || b.state.stopped() {
		t.Fatal("remaining job was falsely confirmed or disabled", b.commands, b.state)
	}
}

func TestSystemdUnknownLoadedConfigurationIsPreserved(t *testing.T) {
	for _, change := range []struct{ key, value string }{
		{"FragmentPath", "/run/systemd/system/foreign.service"}, {"DropInPaths", "/etc/systemd/system/tenebra.service.d/custom.conf"},
		{"SourcePath", "/etc/init.d/foreign"}, {"Transient", "yes"}, {"NeedDaemonReload", "yes"}, {"Delegate", "yes"}, {"User", "1000"}, {"Group", "wheel"},
		{"KillMode", "process"}, {"SendSIGKILL", "no"}, {"Id", "foreign.service"}, {"ControlGroup", "/other.slice/tenebra.service"}, {"MainPID", "+123"}, {"TasksCurrent", "unknown"},
	} {
		t.Run(change.key, func(t *testing.T) {
			job, b, _ := systemdFixture(t)
			loadSystemdFixture(b, "/etc/systemd/system/tenebra.service")
			b.state[change.key] = change.value
			before := append([]byte{}, b.units["/etc/systemd/system/tenebra.service"]...)
			if err := job.Start(context.Background()); !errors.Is(err, ErrUnknownService) {
				t.Fatal("unknown OS job accepted", err)
			}
			if err := job.StopAndWait(context.Background()); !errors.Is(err, ErrUnknownService) {
				t.Fatal("unknown OS job stopped", err)
			}
			if len(b.commands) != 0 || b.publications != 0 || !bytes.Equal(before, b.units["/etc/systemd/system/tenebra.service"]) {
				t.Fatal("unknown custom job mutated", b.commands)
			}
		})
	}
}

func TestSystemdKnownLegacyRequiresSnapshotBeforeAnyNativeAction(t *testing.T) {
	legacy, err := os.ReadFile("testdata/legacy-linux-unit.service")
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range [][]byte{legacy, bytes.ReplaceAll(legacy, []byte("\n"), []byte("\r\n"))} {
		job, b, _ := systemdFixture(t)
		b.units["/etc/systemd/system/tenebra.service"] = body
		if err := job.Start(context.Background()); !errors.Is(err, ErrLegacyAdoptionRequired) {
			t.Fatal("known legacy was not gated on actual snapshot", err)
		}
		if err := job.StopAndWait(context.Background()); !errors.Is(err, ErrLegacyAdoptionRequired) {
			t.Fatal("known legacy stopped before snapshot", err)
		}
		if len(b.commands) != 0 || b.publications != 0 || !bytes.Equal(body, b.units["/etc/systemd/system/tenebra.service"]) {
			t.Fatal("legacy template overwritten")
		}
	}
	job, b, _ := systemdFixture(t)
	b.units["/etc/systemd/system/tenebra.service"] = append(append([]byte{}, legacy...), []byte("# custom\n")...)
	if err := job.VerifyKnown(context.Background()); !errors.Is(err, ErrUnknownService) {
		t.Fatal("modified legacy template was adopted", err)
	}
}

func TestSystemdAdoptedLegacyStopsWholeJobBeforeReplacingUnit(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("isolated native root fixture")
	}
	files, _, input, _ := stagingFixture(t, "flat", "release", false)
	ref, err := files.snapshotLegacyInput(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := os.ReadFile("testdata/legacy-linux-unit.service")
	if err != nil {
		t.Fatal(err)
	}
	for _, keepTasks := range []bool{false, true} {
		job, boundary, disk := systemdFixture(t)
		job.legacyFiles = files
		disk.journal.Active = ref
		disk.journal.Transaction.PriorActive = ref
		disk.journal.Transaction.Phase = "fencing"
		disk.journal.Transaction.SchemaReceipt = nil
		disk.journal.Transaction.Checkpoint = nil
		loadSystemdFixture(boundary, "/etc/systemd/system/tenebra.service")
		boundary.units["/etc/systemd/system/tenebra.service"] = legacy
		boundary.state["ActiveState"], boundary.state["SubState"] = "active", "running"
		boundary.state["MainPID"], boundary.state["ControlGroup"], boundary.state["TasksCurrent"] = "123", "/system.slice/tenebra.service", "2"
		boundary.keepTasks = keepTasks
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		err := job.StopAndWait(ctx)
		cancel()
		if keepTasks {
			if err == nil || !bytes.Equal(boundary.units["/etc/systemd/system/tenebra.service"], legacy) || boundary.publications != 0 ||
				!reflect.DeepEqual(boundary.commands, []string{"stop"}) {
				t.Fatal("unit was replaced while a legacy descendant remained", err, boundary.commands)
			}
			continue
		}
		if err != nil || boundary.publications != 1 || !bytes.Equal(boundary.units["/etc/systemd/system/tenebra.service"], []byte(linuxServiceHostUnit)) ||
			!boundary.state.stopped() || !reflect.DeepEqual(boundary.commands, []string{"stop", "disable", "daemon-reload"}) {
			t.Fatal("legacy handoff did not retain the durable boot fence", err, boundary.commands)
		}
		if plan, err := PlanServiceHost(disk.journal, nil); err != nil || plan.Action != "inactive" {
			t.Fatal("new host could start business before the operational commit", plan, err)
		}
		if err := job.StopAndWait(context.Background()); err != nil || boundary.publications != 1 || len(boundary.commands) != 3 {
			t.Fatal("repeated legacy fence changed the native job", err, boundary.commands)
		}
	}
}

func TestSystemdCrossManagerConflictAndPackageInputsAreReadOnly(t *testing.T) {
	job, b, d := systemdFixture(t)
	b.units["/usr/lib/systemd/system/tenebra.service"] = []byte("package-owned")
	if err := job.Start(context.Background()); !errors.Is(err, ErrConflict) || b.publications != 0 || len(b.commands) != 0 {
		t.Fatal("standalone clobbered package ownership", err)
	}
	delete(b.units, "/usr/lib/systemd/system/tenebra.service")
	d.journal.Manager = "dpkg"
	if err := job.Start(context.Background()); !errors.Is(err, ErrConflict) || b.publications != 0 {
		t.Fatal("helper invented /usr package file", err)
	}
	b.units["/usr/lib/systemd/system/tenebra.service"] = []byte(strings.ReplaceAll(linuxServiceHostUnit, "/usr/local/lib/tenebra/tenebra-helper", "/usr/lib/tenebra/tenebra-helper"))
	b.state["LoadState"] = "loaded"
	b.state["FragmentPath"] = "/usr/lib/systemd/system/tenebra.service"
	b.state["UnitFileState"] = "disabled"
	if err := job.Start(context.Background()); err != nil || b.publications != 0 {
		t.Fatal("known package unit was rewritten or refused", err)
	}
}

func TestSystemdPublishAndCommandFailuresNeverPretendStarted(t *testing.T) {
	job, b, _ := systemdFixture(t)
	b.publishFailure = errFixtureJob
	if err := job.Start(context.Background()); !errors.Is(err, errFixtureJob) || len(b.commands) != 0 {
		t.Fatal("failed publication started native job", err, b.commands)
	}
	job, b, _ = systemdFixture(t)
	b.commandFailure = errFixtureJob
	if err := job.Start(context.Background()); !errors.Is(err, errFixtureJob) || !reflect.DeepEqual(b.commands, []string{"daemon-reload"}) {
		t.Fatal("failed manager command continued to start", err, b.commands)
	}
}

func TestSystemdProductionHostRequiresItsExactNativeJob(t *testing.T) {
	job, b, _ := systemdFixture(t)
	loadSystemdFixture(b, "/etc/systemd/system/tenebra.service")
	if err := job.VerifyHostProcess(context.Background(), 123); !errors.Is(err, ErrUnknownService) {
		t.Fatal("manual root host accepted outside native job", err)
	}
	b.state["ActiveState"] = "active"
	b.state["SubState"] = "running"
	b.state["MainPID"] = "123"
	b.state["ControlGroup"] = "/system.slice/tenebra.service"
	b.state["TasksCurrent"] = "1"
	if err := job.VerifyHostProcess(context.Background(), 123); err != nil {
		t.Fatal("fixed native owner not recognized", err)
	}
	if err := job.VerifyHostProcess(context.Background(), 124); !errors.Is(err, ErrUnknownService) {
		t.Fatal("foreign native process accepted", err)
	}
	if len(b.commands) != 0 || b.publications != 0 {
		t.Fatal("ownership check mutated native job")
	}
}

func TestSystemdPropertyDecoderAndFixedCommandArguments(t *testing.T) {
	state := baseSystemdState()
	var body strings.Builder
	for _, key := range strings.Split(systemdPropertyNames, ",") {
		body.WriteString(key + "=" + state[key] + "\n")
	}
	valid := body.String()
	decoded, err := decodeSystemdState([]byte(valid))
	if err != nil || decoded.validate("/etc/systemd/system/tenebra.service") != nil {
		t.Fatal("actual not-found property shape refused", decoded, err)
	}
	for _, bad := range []string{valid + "Id=tenebra.service\n", strings.Replace(valid, "Id=tenebra.service\n", "", 1), valid + "Unknown=1\n", valid + "\x00", strings.Replace(valid, "Delegate=no", "Delegate=yes\nInjected=1", 1)} {
		if _, err := decodeSystemdState([]byte(bad)); err == nil {
			t.Fatal("malformed native property frame accepted")
		}
	}
	for _, action := range []string{"start", "stop", "enable", "disable"} {
		args, err := systemdArgs(action)
		if err != nil || !reflect.DeepEqual(args, []string{"--system", "--no-pager", "--no-ask-password", action, "--", "tenebra.service"}) {
			t.Fatal("native target/arguments drifted", args, err)
		}
	}
	for _, bad := range []string{"restart", "kill", "--user", "stop foreign.service", "start;sh"} {
		if _, err := systemdArgs(bad); err == nil {
			t.Fatal("arbitrary native action accepted", bad)
		}
	}
}
