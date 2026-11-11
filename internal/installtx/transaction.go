package installtx

import (
	"context"
	"errors"
	"github.com/Divaaaan/tenebra/core/state"
	"io"
	"io/fs"
	"math"
)

type Installer struct {
	fs      FS
	store   StorePort
	service ServiceManager
	probe   ReadyProbe
}

func New(files FS, store StorePort, service ServiceManager, probe ReadyProbe) *Installer {
	return &Installer{fs: files, store: store, service: service, probe: probe}
}

type transactionLocker interface {
	LockTransaction(context.Context) (io.Closer, error)
}
type runtimeManifestReader interface {
	ReadRuntimeManifest(context.Context, RuntimeRef) (Manifest, error)
}

func (i *Installer) lock(ctx context.Context) (io.Closer, error) {
	if i == nil || i.fs == nil || i.store == nil || i.service == nil || i.probe == nil {
		return nil, ErrProtocol
	}
	locker, ok := i.fs.(transactionLocker)
	if !ok {
		return nil, ErrProtocol
	}
	return locker.LockTransaction(ctx)
}
func journalStatus(j Journal) Status {
	s := Status{Phase: "inactive", Manager: j.Manager, BusinessAdmission: "closed"}
	if j.Active != nil {
		s.ActiveVersion = j.Active.Version
	}
	if j.Pending != nil {
		s.PendingVersion = j.Pending.Version
	}
	if tx := j.Transaction; tx != nil {
		s.Phase = tx.Phase
		s.TransactionID = tx.ID
		s.OperationalCommitID = tx.OperationalCommitID
		s.ErrorCode = tx.ErrorCode
		if tx.Phase == "committed_open" {
			s.BusinessAdmission = "open"
		}
		if oneOf(tx.Phase, "pending", "staged") && j.Active != nil {
			s.BusinessAdmission = "unchanged"
		}
	}
	return s
}
func (i *Installer) Status(ctx context.Context) (Status, error) {
	if i == nil || i.fs == nil {
		return Status{}, ErrProtocol
	}
	j, err := i.fs.ReadJournal(ctx)
	if err != nil {
		return Status{}, err
	}
	if err := j.Validate(); err != nil {
		return Status{}, err
	}
	return journalStatus(j), nil
}
func (i *Installer) write(ctx context.Context, j *Journal, phase string) error {
	if j.Generation == math.MaxUint64 || j.Transaction == nil {
		return ErrProtocol
	}
	j.Transaction.Phase = phase
	j.Generation++
	if err := i.fs.ReplaceJournal(ctx, *j); err != nil {
		// Replace may fail after its durable rename. A new owner must reread;
		// an automatic guessed rollback could cross the operational commit.
		return errors.Join(ErrRecoveryRequired, err)
	}
	return nil
}
func resultFor(j Journal, changed bool, err error) (Result, error) {
	return Result{Status: journalStatus(j), Changed: changed}, err
}
func (i *Installer) Apply(ctx context.Context, request ApplyRequest) (Result, error) {
	return i.apply(ctx, request, "")
}

// Package hooks supply a trusted manager separately from request JSON. First
// pacman install stages only; first dpkg configure may activate idle service.
// Every upgrade remains pending until an explicit apply_pending acknowledgement.
func (i *Installer) StagePackage(ctx context.Context, request ApplyRequest, manager string) (Result, error) {
	if !oneOf(manager, "dpkg", "pacman") || request.Action != "install" || request.Trust != "package_manager" || request.RestartAck != "" ||
		request.Source == nil || request.Source.Layout != "installed_package" || request.Source.AbsolutePath != "/usr/lib/tenebra/package-payload" {
		return Result{}, ErrProtocol
	}
	return i.apply(ctx, request, manager)
}

func (i *Installer) apply(ctx context.Context, request ApplyRequest, packageManager string) (Result, error) {
	if err := request.Validate(); err != nil {
		return Result{}, err
	}
	lock, err := i.lock(ctx)
	if err != nil {
		return Result{}, err
	}
	defer lock.Close()
	j, err := i.fs.ReadJournal(ctx)
	if errors.Is(err, fs.ErrNotExist) {
		j = Journal{Schema: 1, Generation: 1, Manager: "standalone"}
		if err := i.fs.ReplaceJournal(ctx, j); err != nil {
			return Result{}, errors.Join(ErrRecoveryRequired, err)
		}
	} else if err != nil {
		return Result{}, err
	}
	if err := j.Validate(); err != nil {
		return Result{}, err
	}
	if packageManager != "" && j.Manager != packageManager {
		return resultFor(j, false, ErrConflict)
	}
	if tx := j.Transaction; tx != nil && !oneOf(tx.Phase, "committed_open", "rolled_back", "removed", "pending", "staged") {
		return resultFor(j, false, ErrRecoveryRequired)
	}
	prior := j.Active
	if prior == nil && j.Pending == nil && oneOf(request.Action, "install", "remove") {
		if legacy, ok := i.fs.(interface {
			SnapshotLegacy(context.Context) (*RuntimeRef, error)
		}); ok {
			ref, err := legacy.SnapshotLegacy(ctx)
			if err != nil {
				return resultFor(j, false, err)
			}
			if ref != nil {
				if j.Manager != "standalone" || !validRuntime(ref) || ref.Provenance != "legacy_root_owned" || ref.Version != "legacy" {
					return resultFor(j, false, ErrProtocol)
				}
				copy := *ref
				prior = &copy
			}
		}
	}
	switch request.Action {
	case "install":
		if packageManager != "" && j.Active != nil && j.Pending == nil && j.Transaction != nil && j.Transaction.Phase == "committed_open" &&
			j.Active.Provenance == "package_manager" && j.Active.ArtifactSHA256 == request.ExpectedPayloadSHA256 && j.Active.Version == request.ExpectedVersion {
			identity, err := i.verifiedPackageHelper(ctx, *j.Active, request)
			if err != nil {
				return resultFor(j, false, err)
			}
			if identity == j.Active.HelperSHA256 {
				return resultFor(j, false, nil)
			}
		}
		if j.Transaction != nil && oneOf(j.Transaction.Phase, "pending", "staged") {
			if packageManager != "" && j.Pending != nil && j.Pending.Provenance == "package_manager" && j.Transaction.Trust == "package_manager" &&
				j.Pending.ArtifactSHA256 == request.ExpectedPayloadSHA256 && j.Pending.Version == request.ExpectedVersion {
				reader, readOK := i.fs.(runtimeManifestReader)
				identity, idOK := i.fs.(interface{ HelperIdentity() string })
				if !readOK || !idOK || !hexValue(identity.HelperIdentity(), 64) {
					return resultFor(j, false, ErrProtocol)
				}
				manifest, err := reader.ReadRuntimeManifest(ctx, *j.Pending)
				if err != nil {
					return resultFor(j, false, err)
				}
				hash, err := artifactHash(manifest)
				if err != nil || hash != request.ExpectedPayloadSHA256 || manifest.Version != request.ExpectedVersion || manifest.SourceSHA != request.ExpectedSourceSHA {
					return resultFor(j, false, ErrConflict)
				}
				if j.Pending.HelperSHA256 == identity.HelperIdentity() && j.Transaction.Phase == "pending" {
					return resultFor(j, false, nil)
				}
				pending := *j.Pending
				pending.HelperSHA256 = identity.HelperIdentity()
				j.Pending = &pending
				j.Transaction.Candidate = &pending
				err = i.write(ctx, &j, "pending")
				return resultFor(j, true, err)
			}
			return resultFor(j, false, ErrConflict)
		}
		payload, err := i.fs.Stage(ctx, *request.Source, Manifest{Schema: 1, Kind: "tenebra-install-payload", Version: request.ExpectedVersion, SourceSHA: request.ExpectedSourceSHA})
		if err != nil {
			return resultFor(j, false, err)
		}
		defer i.fs.RemoveOwnedStage(context.WithoutCancel(ctx), payload.StageID)
		hash, err := artifactHash(payload.Manifest)
		if err != nil || hash != request.ExpectedPayloadSHA256 || payload.Manifest.Version != request.ExpectedVersion || payload.Manifest.SourceSHA != request.ExpectedSourceSHA ||
			payload.Runtime.ArtifactSHA256 != hash || payload.Runtime.Version != request.ExpectedVersion || payload.Runtime.Provenance != request.Trust || !validRuntime(&payload.Runtime) {
			return resultFor(j, false, ErrProtocol)
		}
		if request.Trust == "package_manager" && j.Manager == "standalone" {
			return resultFor(j, false, ErrProtocol)
		}
		runtime, err := i.fs.PublishRuntime(ctx, payload)
		if err != nil {
			return resultFor(j, false, err)
		}
		if runtime != payload.Runtime {
			return resultFor(j, false, ErrProtocol)
		}
		id, err := newID("it1_")
		if err != nil {
			return resultFor(j, false, err)
		}
		j.Active = prior
		j.Pending = &runtime
		j.Transaction = &Transaction{ID: id, Action: "install", Phase: "staged", RestartAck: request.RestartAck, Trust: request.Trust, PriorActive: j.Active, Candidate: &runtime}
		if err := i.write(ctx, &j, "staged"); err != nil {
			return resultFor(j, true, err)
		}
		if (j.Active != nil || packageManager == "pacman") && request.RestartAck == "" {
			err := i.write(ctx, &j, "pending")
			return resultFor(j, true, err)
		}
	case "apply_pending":
		if j.Pending == nil || j.Transaction == nil || !oneOf(j.Transaction.Phase, "pending", "staged") ||
			j.Pending.ArtifactSHA256 != request.ExpectedPayloadSHA256 {
			return resultFor(j, false, ErrConflict)
		}
		j.Transaction.Action = "apply_pending"
		j.Transaction.RestartAck = "apply_pending"
	case "remove":
		id, err := newID("it1_")
		if err != nil {
			return resultFor(j, false, err)
		}
		trust := "operator_dev"
		if j.Active != nil && oneOf(j.Active.Provenance, "release", "package_manager", "operator_dev") {
			trust = j.Active.Provenance
		}
		j.Active = prior
		j.Transaction = &Transaction{ID: id, Action: "remove", Phase: "fencing", Trust: trust, PriorActive: j.Active}
		j.Pending = nil
	}
	if err := i.write(ctx, &j, "fencing"); err != nil {
		return resultFor(j, true, err)
	}
	if err := i.fenceAndStop(ctx, j.Transaction.ID); err != nil {
		return i.block(ctx, j, "fence_failed", err)
	}
	if request.Action == "remove" {
		oc, err := newID("oc1_")
		if err != nil {
			return i.block(ctx, j, "commit_id_failed", err)
		}
		j.Active = nil
		j.Pending = nil
		j.Transaction.OperationalCommitID = oc
		err = i.write(ctx, &j, "removed")
		return resultFor(j, true, err)
	}
	checkpoint, err := i.store.Checkpoint(ctx, j.Transaction.ID)
	if err != nil {
		return i.block(ctx, j, "checkpoint_failed", err)
	}
	j.Transaction.Checkpoint = &checkpoint
	if err := i.write(ctx, &j, "fenced"); err != nil {
		return resultFor(j, true, err)
	}
	receipt, err := i.store.MigrateHeld(ctx, j.Transaction.ID)
	if err != nil {
		return i.block(ctx, j, "migration_uncertain", err)
	}
	j.Transaction.SchemaReceipt = &receipt
	if err := i.write(ctx, &j, "schema_ready"); err != nil {
		return resultFor(j, true, err)
	}
	if err := i.startAndVerify(ctx, j, receipt); err != nil {
		return i.rollback(ctx, j, err)
	}
	if err := i.write(ctx, &j, "ready"); err != nil {
		return resultFor(j, true, err)
	}
	oc, err := newID("oc1_")
	if err != nil {
		return i.rollback(ctx, j, err)
	}
	j.Active = j.Transaction.Candidate
	j.Pending = nil
	j.Transaction.OperationalCommitID = oc
	if err := i.write(ctx, &j, "committed_closed"); err != nil {
		return resultFor(j, true, err)
	}
	return i.admit(ctx, j)
}

func (i *Installer) verifiedPackageHelper(ctx context.Context, ref RuntimeRef, request ApplyRequest) (string, error) {
	reader, readOK := i.fs.(runtimeManifestReader)
	identity, idOK := i.fs.(interface{ HelperIdentity() string })
	if !readOK || !idOK || !hexValue(identity.HelperIdentity(), 64) {
		return "", ErrProtocol
	}
	manifest, err := reader.ReadRuntimeManifest(ctx, ref)
	if err != nil {
		return "", err
	}
	hash, err := artifactHash(manifest)
	if err != nil || hash != request.ExpectedPayloadSHA256 || manifest.Version != request.ExpectedVersion || manifest.SourceSHA != request.ExpectedSourceSHA {
		return "", ErrConflict
	}
	return identity.HelperIdentity(), nil
}
func (i *Installer) fenceAndStop(ctx context.Context, tx string) error {
	fence := i.store.Fence(ctx, tx)
	stop := i.service.StopAndWait(ctx)
	return errors.Join(fence, stop)
}
func (i *Installer) block(ctx context.Context, j Journal, code string, cause error) (Result, error) {
	j.Transaction.ErrorCode = code
	err := i.write(context.WithoutCancel(ctx), &j, "blocked")
	return resultFor(j, true, errors.Join(ErrRecoveryRequired, cause, err))
}
func (i *Installer) startAndVerify(ctx context.Context, j Journal, receipt SchemaReceipt) error {
	tx := j.Transaction
	if tx == nil || tx.Candidate == nil || receipt.Schema != state.SchemaVersion || receipt.Revision == 0 {
		return ErrProtocol
	}
	reader, ok := i.fs.(runtimeManifestReader)
	if !ok {
		return ErrProtocol
	}
	manifest, err := reader.ReadRuntimeManifest(ctx, *tx.Candidate)
	if err != nil {
		return err
	}
	hash, err := artifactHash(manifest)
	if err != nil || hash != tx.Candidate.ArtifactSHA256 || manifest.Version != tx.Candidate.Version {
		return ErrProtocol
	}
	coreSHA := ""
	for _, file := range manifest.Files {
		if file.Name == "tenebra-core" {
			coreSHA = file.SHA256
		}
	}
	launch, err := newID("")
	if err != nil {
		return err
	}
	process, err := i.service.StartHeld(ctx, *tx.Candidate, tx.ID, launch)
	if err != nil {
		return err
	}
	_, err = VerifyReady(ctx, i.probe, ReadyExpectation{Schema: 1, StoreSchema: receipt.Schema, TxID: tx.ID, DaemonVersion: tx.Candidate.Version,
		LaunchID: launch, CoreSHA256: coreSHA, Transport: "unix", BusinessAdmission: "closed", StoreRevision: receipt.Revision, Process: process})
	return err
}
func (i *Installer) admit(ctx context.Context, j Journal) (Result, error) {
	tx := j.Transaction
	if tx == nil || tx.OperationalCommitID == "" || !sameRuntime(j.Active, tx.Candidate) {
		return resultFor(j, false, ErrProtocol)
	}
	if err := i.service.Admit(ctx, tx.ID, tx.OperationalCommitID); err != nil {
		return resultFor(j, true, errors.Join(ErrRecoveryRequired, err))
	}
	if err := i.store.Admit(ctx, tx.ID, tx.OperationalCommitID); err != nil {
		return resultFor(j, true, errors.Join(ErrRecoveryRequired, err))
	}
	j.Transaction.ErrorCode = ""
	err := i.write(ctx, &j, "committed_open")
	return resultFor(j, true, err)
}
