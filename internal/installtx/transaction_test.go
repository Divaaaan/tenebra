package installtx

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/Divaaaan/tenebra/core/state"
)

var errInstallerCrash = errors.New("synthetic installer process crash")

type installDisk struct {
	mu           sync.Mutex
	journal      Journal
	manifest     Manifest
	current      SchemaReceipt
	checkpoints  map[string]SchemaReceipt
	before       map[string]SchemaReceipt
	data         map[string]string
	backup       map[string]map[string]string
	calls        []string
	restoreCount int
	admitted     bool
	running      *RuntimeRef
	ambiguous    bool
	stageEdit    bool
	crashPhase   string
	crashAfter   bool
	admitACKLost bool
}

func cloneJournal(j Journal) Journal {
	data, _ := EncodeJournal(j)
	copy, _ := DecodeJournal(data)
	return copy
}
func copyData(data map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range data {
		out[k] = v
	}
	return out
}
func installerDisk() *installDisk {
	m := Manifest{Schema: 1, Kind: "tenebra-install-payload", Version: "0.6.0", SourceSHA: strings.Repeat("a", 40), Target: "linux-amd64"}
	for name, mode := range payloadRoles {
		m.Files = append(m.Files, ManifestFile{Name: name, SHA256: strings.Repeat("b", 64), Size: 17, Mode: mode})
	}
	old := testRuntime()
	old.Version = "0.5.0"
	old.ArtifactSHA256 = strings.Repeat("9", 64)
	initial := Journal{Schema: 1, Generation: 1, Manager: "standalone", Active: &old, Transaction: &Transaction{
		ID: "it1_" + strings.Repeat("1", 32), Action: "install", Phase: "committed_open", OperationalCommitID: "oc1_" + strings.Repeat("2", 32), Trust: "release", Candidate: &old,
		Checkpoint:    &CheckpointRef{ID: "old_checkpoint", Revision: 39, SHA256: strings.Repeat("c", 64)},
		SchemaReceipt: &SchemaReceipt{Schema: 2, MigrationID: "migration_fixture", Revision: 40, CanonicalSHA256: strings.Repeat("d", 64)}}}
	return &installDisk{journal: initial, manifest: m, current: *initial.Transaction.SchemaReceipt, checkpoints: map[string]SchemaReceipt{}, before: map[string]SchemaReceipt{}, data: map[string]string{"profile": "before", "preference": "before"}, backup: map[string]map[string]string{}, admitted: true, running: &old}
}

type fakeInstallFS struct{ disk *installDisk }
type fakeInstallStore struct{ disk *installDisk }
type fakeInstallService struct{ disk *installDisk }
type fakeInstallProbe struct{ disk *installDisk }
type fakeInstallLock struct{ mu *sync.Mutex }

func (l fakeInstallLock) Close() error { l.mu.Unlock(); return nil }
func (f fakeInstallFS) LockTransaction(context.Context) (io.Closer, error) {
	if !f.disk.mu.TryLock() {
		return nil, ErrConflict
	}
	return fakeInstallLock{&f.disk.mu}, nil
}
func (f fakeInstallFS) Stage(ctx context.Context, s Source, m Manifest) (StagedPayload, error) {
	d := f.disk
	d.calls = append(d.calls, "stage")
	if d.stageEdit {
		d.current.Revision = 41
		d.data["profile"] = "edited-while-staging"
	}
	hash, _ := artifactHash(d.manifest)
	return StagedPayload{StageID: "stage_fixture", Manifest: d.manifest, Runtime: RuntimeRef{ArtifactSHA256: hash, Version: d.manifest.Version, HelperSHA256: strings.Repeat("c", 64), Provenance: "release"}}, nil
}
func (f fakeInstallFS) ReadJournal(context.Context) (Journal, error) {
	return cloneJournal(f.disk.journal), nil
}
func (f fakeInstallFS) ReplaceJournal(ctx context.Context, j Journal) error {
	d := f.disk
	if err := j.Validate(); err != nil {
		return err
	}
	if j.Generation != d.journal.Generation+1 {
		return ErrConflict
	}
	if d.crashPhase == j.Transaction.Phase && !d.crashAfter {
		d.crashPhase = ""
		return errInstallerCrash
	}
	d.journal = cloneJournal(j)
	d.calls = append(d.calls, "journal:"+j.Transaction.Phase)
	if d.crashPhase == j.Transaction.Phase && d.crashAfter {
		d.crashPhase = ""
		return errInstallerCrash
	}
	return nil
}
func (f fakeInstallFS) PublishRuntime(ctx context.Context, s StagedPayload) (RuntimeRef, error) {
	f.disk.calls = append(f.disk.calls, "publish")
	return s.Runtime, nil
}
func (f fakeInstallFS) RemoveOwnedStage(context.Context, string) error { return nil }
func (f fakeInstallFS) ReadRuntimeManifest(ctx context.Context, r RuntimeRef) (Manifest, error) {
	return f.disk.manifest, nil
}
func (s fakeInstallStore) Fence(context.Context, string) error {
	s.disk.admitted = false
	s.disk.calls = append(s.disk.calls, "fence")
	return nil
}
func (s fakeInstallStore) Checkpoint(ctx context.Context, tx string) (CheckpointRef, error) {
	d := s.disk
	if d.admitted || d.running != nil {
		return CheckpointRef{}, errors.New("checkpoint before drain/stop")
	}
	d.calls = append(d.calls, "checkpoint")
	d.checkpoints[tx] = d.current
	d.backup[tx] = copyData(d.data)
	return CheckpointRef{ID: "checkpoint_fixture", Revision: d.current.Revision, SHA256: strings.Repeat("e", 64)}, nil
}
func (s fakeInstallStore) MigrateHeld(ctx context.Context, tx string) (SchemaReceipt, error) {
	d := s.disk
	if d.ambiguous {
		return SchemaReceipt{}, state.ErrDurabilityUnknown
	}
	if d.admitted || d.running != nil {
		return SchemaReceipt{}, errors.New("migration before drain/stop")
	}
	d.calls = append(d.calls, "migrate")
	if _, ok := d.before[tx]; !ok {
		d.before[tx] = d.current
		d.current.Revision++
		d.current.CanonicalSHA256 = strings.Repeat("f", 64)
	}
	return d.current, nil
}
func (s fakeInstallStore) RestoreHeld(ctx context.Context, tx string, ref CheckpointRef) error {
	d := s.disk
	if d.admitted || d.running != nil {
		return errors.New("restore while a writer/process is active")
	}
	d.calls = append(d.calls, "restore")
	d.restoreCount++
	d.current = d.checkpoints[tx]
	d.data = copyData(d.backup[tx])
	return nil
}
func (s fakeInstallStore) Admit(ctx context.Context, tx, oc string) error {
	d := s.disk
	if d.journal.Transaction.ID != tx || d.journal.Transaction.OperationalCommitID != oc || !sameRuntime(d.journal.Active, d.journal.Transaction.Candidate) {
		return errors.New("admit before durable operational commit")
	}
	d.calls = append(d.calls, "store-admit:"+oc)
	d.admitted = true
	return nil
}
func (s fakeInstallStore) Inspect(context.Context) (SchemaReceipt, error) {
	if s.disk.ambiguous {
		return SchemaReceipt{}, state.ErrDurabilityUnknown
	}
	return s.disk.current, nil
}
func (s fakeInstallService) StopAndWait(context.Context) error {
	s.disk.running = nil
	s.disk.calls = append(s.disk.calls, "stop")
	return nil
}
func (s fakeInstallService) StartHeld(ctx context.Context, r RuntimeRef, tx, launch string) (ProcessIdentity, error) {
	d := s.disk
	if d.admitted {
		return ProcessIdentity{}, errors.New("start with business admitted")
	}
	d.running = &r
	d.calls = append(d.calls, "start:"+r.Version)
	return ProcessIdentity{PID: 123, StartToken: "synthetic-start", LaunchID: launch}, nil
}
func (s fakeInstallService) Admit(ctx context.Context, tx, oc string) error {
	if s.disk.journal.Transaction.ID != tx || s.disk.journal.Transaction.OperationalCommitID != oc {
		return errors.New("service admit before commit")
	}
	s.disk.calls = append(s.disk.calls, "service-admit:"+oc)
	if s.disk.admitACKLost {
		s.disk.admitACKLost = false
		s.disk.admitted = true
		s.disk.current.Revision++
		s.disk.current.CanonicalSHA256 = strings.Repeat("6", 64)
		s.disk.data["write-before-admit-ack"] = "kept"
		return errInstallerCrash
	}
	return nil
}
func (p fakeInstallProbe) Exchange(ctx context.Context, process ProcessIdentity, r ReadyRequest) (ReadyReply, error) {
	d := p.disk
	return ReadyReply{Schema: 1, StoreSchema: 2, Status: "ready", TxID: r.TxID, Challenge: r.Challenge, DaemonVersion: d.running.Version,
		DaemonInstanceID: "synthetic_new_process_instance", LaunchID: process.LaunchID, CoreSHA256: strings.Repeat("b", 64), Transport: "unix", BusinessAdmission: "closed", StoreRevision: d.current.Revision}, nil
}
func reconstructedInstaller(d *installDisk) *Installer {
	return New(fakeInstallFS{d}, fakeInstallStore{d}, fakeInstallService{d}, fakeInstallProbe{d})
}
func installRequest(d *installDisk) ApplyRequest {
	hash, _ := artifactHash(d.manifest)
	return ApplyRequest{Schema: 1, OperationID: "so1_" + strings.Repeat("3", 32), Action: "install", RestartAck: "restart", Trust: "release",
		ExpectedVersion: d.manifest.Version, ExpectedSourceSHA: d.manifest.SourceSHA, ExpectedPayloadSHA256: hash,
		Source: &Source{Layout: "flat", AbsolutePath: "synthetic-stage", SignaturePath: "synthetic-stage.sig"}}
}
func TestUpgradeDoesNotCheckpointBeforeFence(t *testing.T) {
	d := installerDisk()
	d.stageEdit = true
	if _, err := reconstructedInstaller(d).Apply(context.Background(), installRequest(d)); err != nil {
		t.Fatal(err, d.calls)
	}
	tx := d.journal.Transaction
	if tx.Checkpoint.Revision != 41 || tx.Phase != "committed_open" || !d.admitted || d.data["profile"] != "edited-while-staging" {
		t.Fatal("stale checkpoint or lost concurrent edit", tx, d.data, d.calls)
	}
}
func TestStagingDoesNotFenceOrCheckpointWithoutRestart(t *testing.T) {
	d := installerDisk()
	request := installRequest(d)
	request.RestartAck = ""
	result, err := reconstructedInstaller(d).Apply(context.Background(), request)
	if err != nil || result.Status.Phase != "pending" || !d.admitted || len(d.checkpoints) != 0 || d.running.Version != "0.5.0" {
		t.Fatal(result, err, d.calls)
	}
}
func TestCrashAfterSchemaReplaceBeforeOperationalCommit(t *testing.T) {
	d := installerDisk()
	d.crashPhase = "schema_ready"
	if _, err := reconstructedInstaller(d).Apply(context.Background(), installRequest(d)); err == nil {
		t.Fatal("injected process crash ignored")
	}
	if d.journal.Transaction.Phase != "fenced" || d.current.Revision != 41 || d.admitted {
		t.Fatal("wrong crash boundary", d.calls)
	}
	if _, err := reconstructedInstaller(d).Recover(context.Background()); err != nil {
		t.Fatal(err, d.calls)
	}
	if d.journal.Active.Version != "0.5.0" || d.current.Revision != 40 || d.restoreCount != 1 || d.admitted || d.journal.Transaction.Phase != "rolled_back" {
		t.Fatal("whole precommit restore failed", d.calls)
	}
}
func TestAmbiguousSchemaReplaceBlocksStartup(t *testing.T) {
	d := installerDisk()
	d.crashPhase = "schema_ready"
	reconstructedInstaller(d).Apply(context.Background(), installRequest(d))
	d.ambiguous = true
	if _, err := reconstructedInstaller(d).Recover(context.Background()); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatal(err)
	}
	if d.journal.Transaction.Phase != "blocked" || d.restoreCount != 0 || d.admitted || d.running != nil {
		t.Fatal("guessed restore/start", d.calls)
	}
}
func TestCrashAfterOperationalCommitBeforeAdmission(t *testing.T) {
	d := installerDisk()
	d.crashPhase = "committed_closed"
	d.crashAfter = true
	if _, err := reconstructedInstaller(d).Apply(context.Background(), installRequest(d)); err == nil {
		t.Fatal("injected process crash ignored")
	}
	oc := d.journal.Transaction.OperationalCommitID
	if oc == "" || d.journal.Active.Version != "0.6.0" || d.admitted {
		t.Fatal("operational commit not atomic", d.calls)
	}
	d.calls = nil
	if _, err := reconstructedInstaller(d).Recover(context.Background()); err != nil {
		t.Fatal(err, d.calls)
	}
	if d.restoreCount != 0 || !d.admitted || d.journal.Transaction.OperationalCommitID != oc || d.running.Version != "0.6.0" {
		t.Fatal("postcommit restored old state/runtime or changed admit ID", d.calls)
	}
	for _, call := range d.calls {
		if call == "start:0.5.0" || call == "migrate" {
			t.Fatal("old runtime/schema replay after commit", d.calls)
		}
	}
}
func TestBusinessWritesSurvivePostCommitRestart(t *testing.T) {
	d := installerDisk()
	if _, err := reconstructedInstaller(d).Apply(context.Background(), installRequest(d)); err != nil {
		t.Fatal(err)
	}
	d.current.Revision = 80
	d.current.CanonicalSHA256 = strings.Repeat("7", 64)
	d.data["new-profile-immutable-id"] = "kept"
	d.data["preference"] = "later"
	oc := d.journal.Transaction.OperationalCommitID
	if _, err := reconstructedInstaller(d).Recover(context.Background()); err != nil {
		t.Fatal(err, d.calls)
	}
	if d.current.Revision != 80 || d.data["new-profile-immutable-id"] != "kept" || d.data["preference"] != "later" || d.restoreCount != 0 || d.journal.Transaction.OperationalCommitID != oc {
		t.Fatal("postcommit data reverted", d.calls)
	}
}
func TestRemoveCommitNeverReenablesService(t *testing.T) {
	d := installerDisk()
	request := ApplyRequest{Schema: 1, OperationID: "so1_" + strings.Repeat("4", 32), Action: "remove", KeepProfiles: true}
	if _, err := reconstructedInstaller(d).Apply(context.Background(), request); err != nil {
		t.Fatal(err, d.calls)
	}
	if d.journal.Active != nil || d.running != nil || d.admitted || d.journal.Transaction.Phase != "removed" || d.data["profile"] != "before" {
		t.Fatal("removal modified profiles or retained runtime", d.calls)
	}
	if _, err := reconstructedInstaller(d).Recover(context.Background()); err != nil {
		t.Fatal(err, d.calls)
	}
	for _, call := range d.calls {
		if strings.HasPrefix(call, "start:") || strings.Contains(call, "admit:") || call == "migrate" || call == "restore" || call == "checkpoint" {
			t.Fatal("remove entered activation branch", d.calls)
		}
	}
}

func TestCrashDuringWholeRollbackResumesWithoutMigration(t *testing.T) {
	d := installerDisk()
	d.crashPhase = "schema_ready"
	if _, err := reconstructedInstaller(d).Apply(context.Background(), installRequest(d)); err == nil {
		t.Fatal("crash ignored")
	}
	d.crashPhase = "rolled_back"
	if _, err := reconstructedInstaller(d).Recover(context.Background()); err == nil {
		t.Fatal("rollback crash ignored")
	}
	if d.current.Revision != 40 || d.journal.Transaction.Phase != "rolling_back" {
		t.Fatal("wrong rollback crash boundary", d.calls)
	}
	d.calls = nil
	if _, err := reconstructedInstaller(d).Recover(context.Background()); err != nil {
		t.Fatal(err, d.calls)
	}
	if d.current.Revision != 40 || d.admitted || d.journal.Active.Version != "0.5.0" || d.journal.Transaction.Phase != "rolled_back" {
		t.Fatal("whole restore not resumed", d.calls)
	}
	for _, call := range d.calls {
		if call == "migrate" {
			t.Fatal("restored state migrated while resuming rollback", d.calls)
		}
	}
}

func TestLostAdmissionACKPreservesAlreadyCommittedWrites(t *testing.T) {
	d := installerDisk()
	d.admitACKLost = true
	if _, err := reconstructedInstaller(d).Apply(context.Background(), installRequest(d)); err == nil {
		t.Fatal("lost ACK ignored")
	}
	oc := d.journal.Transaction.OperationalCommitID
	if !d.admitted || d.data["write-before-admit-ack"] != "kept" || oc == "" {
		t.Fatal("fixture did not write after operational commit", d.calls)
	}
	if _, err := reconstructedInstaller(d).Recover(context.Background()); err != nil {
		t.Fatal(err, d.calls)
	}
	if !d.admitted || d.data["write-before-admit-ack"] != "kept" || d.restoreCount != 0 || d.journal.Transaction.OperationalCommitID != oc {
		t.Fatal("lost ACK rolled back committed writes", d.calls)
	}
}
