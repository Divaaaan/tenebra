//go:build linux

package installtx

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Divaaaan/tenebra/internal/securefs"
)

// The hardening follows the existing standalone unit. Business readiness is
// exclusively the authenticated private protocol; Type=simple is never proof.
//
//go:embed systemd/tenebra.service
var linuxPackagedServiceHostUnit string

var linuxServiceHostUnit = strings.Replace(linuxPackagedServiceHostUnit,
	"/usr/lib/tenebra/tenebra-helper", "/usr/local/lib/tenebra/tenebra-helper", 1)

const systemdPropertyNames = "Id,LoadState,FragmentPath,DropInPaths,SourcePath,Transient,NeedDaemonReload,ActiveState,SubState,MainPID,ControlPID,ControlGroup,TasksCurrent,KillMode,SendSIGKILL,Delegate,User,Group,UnitFileState"

type systemdJobState map[string]string

func decodeSystemdState(data []byte) (systemdJobState, error) {
	if len(data) == 0 || len(data) > MaxMetadataBytes || !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return nil, ErrServiceManager
	}
	allowed := strings.Split(systemdPropertyNames, ",")
	state := systemdJobState{}
	for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok || !oneOf(key, allowed...) || strings.ContainsAny(value, "\r\x00") {
			return nil, ErrServiceManager
		}
		if _, exists := state[key]; exists {
			return nil, ErrServiceManager
		}
		state[key] = value
	}
	if len(state) != len(allowed) {
		return nil, ErrServiceManager
	}
	return state, nil
}

func (s systemdJobState) validate(path string) error {
	for _, key := range strings.Split(systemdPropertyNames, ",") {
		if _, ok := s[key]; !ok {
			return ErrUnknownService
		}
	}
	if len(s) != len(strings.Split(systemdPropertyNames, ",")) || s["Id"] != "tenebra.service" || s["Transient"] != "no" || s["DropInPaths"] != "" ||
		s["SourcePath"] != "" || s["NeedDaemonReload"] != "no" || s["Delegate"] != "no" || s["User"] != "" || s["Group"] != "" ||
		s["KillMode"] != "control-group" || s["SendSIGKILL"] != "yes" || !oneOf(s["LoadState"], "loaded", "not-found") ||
		!oneOf(s["ActiveState"], "inactive", "failed", "active", "activating", "deactivating") ||
		!oneOf(s["SubState"], "dead", "failed", "running", "start", "start-pre", "start-post", "stop", "stop-sigterm", "stop-sigkill", "stop-post", "auto-restart") {
		return ErrUnknownService
	}
	for _, key := range []string{"MainPID", "ControlPID"} {
		n, err := strconv.ParseUint(s[key], 10, 31)
		if err != nil || strconv.FormatUint(n, 10) != s[key] {
			return ErrUnknownService
		}
	}
	if s["TasksCurrent"] != "[not set]" {
		n, err := strconv.ParseUint(s["TasksCurrent"], 10, 64)
		if err != nil || strconv.FormatUint(n, 10) != s["TasksCurrent"] {
			return ErrUnknownService
		}
	}
	if s["ControlGroup"] != "" && s["ControlGroup"] != "/system.slice/tenebra.service" {
		return ErrUnknownService
	}
	if s["LoadState"] == "loaded" && s["FragmentPath"] != path {
		return ErrUnknownService
	}
	if s["LoadState"] == "not-found" && (s["FragmentPath"] != "" || !s.stopped() || s["ActiveState"] != "inactive" || s["SubState"] != "dead") {
		return ErrUnknownService
	}
	return nil
}
func (s systemdJobState) stopped() bool {
	if !oneOf(s["ActiveState"], "inactive", "failed") || s["MainPID"] != "0" || s["ControlPID"] != "0" {
		return false
	}
	return s["ControlGroup"] == "" && oneOf(s["TasksCurrent"], "0", "[not set]") || s["ControlGroup"] == "/system.slice/tenebra.service" && s["TasksCurrent"] == "0"
}

type systemdBoundary interface {
	ReadUnit(context.Context, string) ([]byte, error)
	PublishUnit(context.Context, string, []byte, string) error
	Query(context.Context) (systemdJobState, error)
	Command(context.Context, string) error
}
type nativeSystemdBoundary struct{}

func systemdUnitPath(path string) bool {
	return path == "/etc/systemd/system/tenebra.service" || path == "/usr/lib/systemd/system/tenebra.service"
}
func (nativeSystemdBoundary) ReadUnit(ctx context.Context, path string) ([]byte, error) {
	if !systemdUnitPath(path) {
		return nil, ErrProtocol
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root, err := securefs.OpenRoot(filepath.Dir(path), securefs.Policy{Mode: securefs.OperatorInput, Principal: "uid:0"}, false)
	if err != nil {
		return nil, err
	}
	body, readErr := root.ReadFile(filepath.Base(path), MaxMetadataBytes)
	return body, errors.Join(readErr, root.Close())
}
func (nativeSystemdBoundary) PublishUnit(ctx context.Context, path string, body []byte, previous string) error {
	// Package-owned /usr is ALWAYS read-only to this helper.
	if path != "/etc/systemd/system/tenebra.service" || !bytes.Equal(body, []byte(linuxServiceHostUnit)) {
		return ErrProtocol
	}
	root, err := securefs.OpenRoot(filepath.Dir(path), securefs.Policy{Mode: securefs.MachineContainer}, false)
	if err != nil {
		return err
	}
	state, err := root.PublishMachineMetadata(ctx, filepath.Base(path), body, previous, 0644)
	closeErr := root.Close()
	if state != securefs.Committed || err != nil || closeErr != nil {
		return errors.Join(ErrRecoveryRequired, err, closeErr)
	}
	return nil
}
func systemdArgs(action string) ([]string, error) {
	args := []string{"--system", "--no-pager", "--no-ask-password"}
	if action == "show" {
		return append(args, "show", "--property="+systemdPropertyNames, "--", "tenebra.service"), nil
	}
	if action == "daemon-reload" {
		return append(args, action), nil
	}
	if !oneOf(action, "start", "stop", "enable", "disable") {
		return nil, ErrProtocol
	}
	return append(args, action, "--", "tenebra.service"), nil
}
func (nativeSystemdBoundary) Query(ctx context.Context) (systemdJobState, error) {
	args, _ := systemdArgs("show")
	r, err := runServiceCommand(ctx, "/usr/bin/systemctl", args...)
	if err != nil {
		return nil, err
	}
	s, err := decodeSystemdState(r.Output)
	if err != nil || r.Code != 0 {
		return nil, errors.Join(ErrServiceManager, err)
	}
	return s, nil
}
func (nativeSystemdBoundary) Command(ctx context.Context, action string) error {
	args, err := systemdArgs(action)
	if err != nil {
		return err
	}
	r, err := runServiceCommand(ctx, "/usr/bin/systemctl", args...)
	if err != nil || r.Code != 0 {
		return errors.Join(ErrServiceManager, err)
	}
	return nil
}

type LinuxServiceHostJob struct {
	journal     JournalReader
	boundary    systemdBoundary
	legacyFiles *ProtectedFS
}

func OpenLinuxServiceHostJob(journal JournalReader) (*LinuxServiceHostJob, error) {
	if os.Geteuid() != 0 || journal == nil {
		return nil, ErrProtocol
	}
	return &LinuxServiceHostJob{journal: journal, boundary: nativeSystemdBoundary{}}, nil
}

type systemdInspection struct {
	journal Journal
	path    string
	body    []byte
	state   systemdJobState
	legacy  bool
}

func (j *LinuxServiceHostJob) inspect(ctx context.Context) (systemdInspection, error) {
	return j.inspectJob(ctx, false)
}

func knownLegacyLinuxUnit(body []byte) bool {
	sum := sha256.Sum256(body)
	return oneOf(hex.EncodeToString(sum[:]), "64598f7fec11ff7263115a683ad45ea3ce2a49d78ccd36e5415e79025dea57d7", "08c2568a4ec3c35e9d52570445391d918ae883d0985f078a99d9b4a2ef2647cb")
}

func (j *LinuxServiceHostJob) inspectJob(ctx context.Context, snapshotOnly bool) (systemdInspection, error) {
	var result systemdInspection
	if j == nil || j.journal == nil || j.boundary == nil {
		return result, ErrProtocol
	}
	journal, err := j.journal.ReadJournal(ctx)
	if err != nil || journal.Validate() != nil {
		return result, errors.Join(ErrRecoveryRequired, err)
	}
	path, other := "/etc/systemd/system/tenebra.service", "/usr/lib/systemd/system/tenebra.service"
	expected := []byte(linuxServiceHostUnit)
	if journal.Manager != "standalone" {
		path, other = other, path
		expected = []byte(linuxPackagedServiceHostUnit)
	}
	if _, err := j.boundary.ReadUnit(ctx, other); err == nil {
		return result, ErrConflict
	} else if !errors.Is(err, fs.ErrNotExist) {
		return result, errors.Join(ErrUnknownService, err)
	}
	body, err := j.boundary.ReadUnit(ctx, path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return result, errors.Join(ErrUnknownService, err)
	}
	legacy := err == nil && journal.Manager == "standalone" && knownLegacyLinuxUnit(body)
	if err == nil && !bytes.Equal(body, expected) && !legacy {
		return result, ErrUnknownService
	}
	if legacy && !snapshotOnly {
		tx := journal.Transaction
		if j.legacyFiles == nil || tx == nil || tx.PriorActive == nil || tx.PriorActive.Provenance != "legacy_root_owned" {
			return result, ErrLegacyAdoptionRequired
		}
		if _, err := j.legacyFiles.ReadRuntimeManifest(ctx, *tx.PriorActive); err != nil {
			return result, errors.Join(ErrLegacyAdoptionRequired, err)
		}
	}
	state, err := j.boundary.Query(ctx)
	if err != nil {
		return result, err
	}
	if err := state.validate(path); err != nil {
		return result, err
	}
	if state["LoadState"] == "loaded" && body == nil {
		return result, ErrUnknownService
	}
	if journal.Manager != "standalone" && body == nil {
		return result, ErrConflict
	}
	return systemdInspection{journal: journal, path: path, body: body, state: state, legacy: legacy}, nil
}

func (j *LinuxServiceHostJob) configureLegacy(files *ProtectedFS) {
	j.legacyFiles = files
	files.legacy = j.snapshotLegacy
}

func (j *LinuxServiceHostJob) snapshotLegacy(ctx context.Context) (reference *RuntimeRef, failure error) {
	inspection, err := j.inspectJob(ctx, true)
	if err != nil || !inspection.legacy {
		return nil, err
	}
	if j.legacyFiles == nil || inspection.journal.Manager != "standalone" || inspection.journal.Active != nil || inspection.journal.Pending != nil {
		return nil, ErrLegacyAdoptionRequired
	}
	input, err := securefs.OpenRoot("/usr/local/lib/tenebra", securefs.Policy{Mode: securefs.OperatorInput, Principal: "uid:0"}, false)
	if err != nil {
		return nil, err
	}
	defer func() { failure = errors.Join(failure, input.Close()) }()
	reference, err = j.legacyFiles.snapshotLegacyInput(ctx, input)
	if err != nil {
		return nil, err
	}
	fresh, err := j.inspectJob(ctx, true)
	if err != nil || !fresh.legacy || fresh.journal.Generation != inspection.journal.Generation || !bytes.Equal(fresh.body, inspection.body) {
		return nil, errors.Join(ErrConflict, err)
	}
	return reference, nil
}
func (j *LinuxServiceHostJob) VerifyKnown(ctx context.Context) error {
	_, err := j.inspect(ctx)
	return err
}

func (j *LinuxServiceHostJob) VerifyHostProcess(ctx context.Context, pid uint32) error {
	if pid == 0 || pid > 1<<31-1 {
		return ErrReadiness
	}
	for {
		inspection, err := j.inspect(ctx)
		if err != nil {
			return err
		}
		if inspection.legacy {
			return ErrUnknownService
		}
		s := inspection.state
		if s["LoadState"] != "loaded" || !oneOf(s["ActiveState"], "active", "activating") || s["ControlGroup"] != "/system.slice/tenebra.service" {
			return ErrUnknownService
		}
		if s["MainPID"] == strconv.FormatUint(uint64(pid), 10) {
			return nil
		}
		if s["MainPID"] != "0" {
			return ErrUnknownService
		}
		if err := hostPoll(ctx); err != nil {
			return err
		}
	}
}

func verifyServiceHostOwner(ctx context.Context, journal *JournalFile) error {
	job, err := OpenLinuxServiceHostJob(journal)
	if err != nil {
		return err
	}
	return job.VerifyHostProcess(ctx, uint32(os.Getpid()))
}
func (j *LinuxServiceHostJob) Start(ctx context.Context) error {
	inspection, err := j.inspect(ctx)
	if err != nil {
		return err
	}
	if !inspection.state.stopped() {
		return ErrRecoveryRequired
	}
	if inspection.legacy {
		return ErrRecoveryRequired
	}
	plan, err := PlanServiceHost(inspection.journal, nil)
	if err != nil || plan.Action != "start_held" {
		return errors.Join(ErrRecoveryRequired, err)
	}
	if inspection.body == nil {
		if inspection.journal.Manager != "standalone" {
			return ErrConflict
		}
		if err := j.boundary.PublishUnit(ctx, inspection.path, []byte(linuxServiceHostUnit), ""); err != nil {
			return err
		}
	}
	if err := j.boundary.Command(ctx, "daemon-reload"); err != nil {
		return err
	}
	fresh, err := j.inspect(ctx)
	if err != nil {
		return err
	}
	if fresh.journal.Generation != inspection.journal.Generation || fresh.state["LoadState"] != "loaded" || !fresh.state.stopped() {
		return ErrRecoveryRequired
	}
	if err := j.boundary.Command(ctx, "enable"); err != nil {
		return err
	}
	return j.boundary.Command(ctx, "start") // NOT a readiness/admission receipt.
}
func (j *LinuxServiceHostJob) StopAndWait(ctx context.Context) error {
	inspection, err := j.inspect(ctx)
	if err != nil {
		return err
	}
	if !hostStopPhase(inspection.journal) {
		return ErrRecoveryRequired
	}
	bound, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if !inspection.state.stopped() {
		if err := j.boundary.Command(bound, "stop"); err != nil {
			return err
		}
	}
	for {
		fresh, err := j.inspect(bound)
		if err != nil {
			return err
		}
		if fresh.journal.Generation != inspection.journal.Generation {
			return ErrConflict
		}
		if fresh.state.stopped() {
			if (inspection.legacy || inspection.journal.Transaction.Action == "remove") && fresh.state["LoadState"] == "loaded" && fresh.state["UnitFileState"] != "disabled" {
				if err := j.boundary.Command(bound, "disable"); err != nil {
					return err
				}
				last, err := j.inspect(bound)
				if err != nil {
					return err
				}
				if !last.state.stopped() || last.state["UnitFileState"] != "disabled" || last.journal.Generation != inspection.journal.Generation {
					return ErrRecoveryRequired
				}
			}
			if fresh.legacy {
				// Replace only after the protected snapshot and whole-job stop.
				// A later boot now observes the durable transaction fence before
				// any business process can start, including precommit rollback.
				sum := sha256.Sum256(fresh.body)
				if err := j.boundary.PublishUnit(bound, fresh.path, []byte(linuxServiceHostUnit), hex.EncodeToString(sum[:])); err != nil {
					return err
				}
				if err := j.boundary.Command(bound, "daemon-reload"); err != nil {
					return err
				}
				last, err := j.inspect(bound)
				if err != nil {
					return err
				}
				if last.legacy || !last.state.stopped() || last.state["UnitFileState"] != "disabled" || last.journal.Generation != inspection.journal.Generation {
					return ErrRecoveryRequired
				}
			}
			return nil
		}
		timer := time.NewTimer(200 * time.Millisecond)
		select {
		case <-bound.Done():
			timer.Stop()
			return errors.Join(ErrServiceManager, bound.Err())
		case <-timer.C:
		}
	}
}
