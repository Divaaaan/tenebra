package installtx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"math"

	"github.com/Divaaaan/tenebra/internal/securefs"
)

type LaunchRequest struct {
	TxID     string     `json:"tx_id"`
	Runtime  RuntimeRef `json:"runtime"`
	LaunchID string     `json:"launch_id"`
	Purpose  string     `json:"purpose"`
}
type LaunchRecord struct {
	Schema              uint32         `json:"schema"`
	Generation          uint64         `json:"generation"`
	State               string         `json:"state"`
	Request             *LaunchRequest `json:"request"`
	OperationalCommitID string         `json:"operational_commit_id"`
}

func (r LaunchRequest) Validate() error {
	if !protocolID(r.TxID, "it1_") || !hexValue(r.LaunchID, 32) || !validRuntime(&r.Runtime) || !oneOf(r.Purpose, "transaction", "boot") {
		return ErrProtocol
	}
	return nil
}
func (r LaunchRecord) Validate() error {
	if r.Schema != 1 || r.Generation == 0 {
		return ErrProtocol
	}
	switch r.State {
	case "idle":
		if r.Request != nil || r.OperationalCommitID != "" {
			return ErrProtocol
		}
	case "prepared", "reserved":
		if r.Request == nil || r.Request.Validate() != nil || r.OperationalCommitID != "" {
			return ErrProtocol
		}
	case "released":
		if r.Request == nil || r.Request.Validate() != nil || r.Request.Purpose != "transaction" || !protocolID(r.OperationalCommitID, "oc1_") {
			return ErrProtocol
		}
	default:
		return ErrProtocol
	}
	return nil
}
func (p *ProcessRecordFile) ReadLaunch(ctx context.Context) (LaunchRecord, error) {
	if p == nil || p.root == nil {
		return LaunchRecord{}, ErrProtocol
	}
	if err := ctx.Err(); err != nil {
		return LaunchRecord{}, err
	}
	data, err := p.root.ReadAtomicFile(ctx, "launch.json", MaxMetadataBytes)
	if errors.Is(err, fs.ErrNotExist) {
		return LaunchRecord{Schema: 1, State: "idle"}, nil
	}
	if err != nil {
		return LaunchRecord{}, err
	}
	var r LaunchRecord
	if strictDecode(data, &r) != nil || r.Validate() != nil {
		return LaunchRecord{}, ErrRecoveryRequired
	}
	return r, nil
}
func launchAllowed(ctx context.Context, journal JournalReader, request LaunchRequest) error {
	if journal == nil || request.Validate() != nil {
		return ErrProtocol
	}
	j, err := journal.ReadJournal(ctx)
	if err != nil {
		return err
	}
	plan, err := PlanServiceHost(j, nil)
	if err != nil || plan.Action != "start_held" || plan.Runtime == nil || plan.TxID != request.TxID || *plan.Runtime != request.Runtime ||
		request.Purpose == "boot" && plan.OperationalCommitID == "" {
		return errors.Join(ErrRecoveryRequired, err)
	}
	return nil
}

// PrepareLaunch is used after the native owner has confirmed no old OS job is
// running (or on an ordinary clean boot under the exclusive host owner lock).
// It cannot overwrite an unresolved reservation. Recovery must prove actual
// process/job termination before any separate explicit reset is introduced.
func (p *ProcessRecordFile) PrepareLaunch(ctx context.Context, journal JournalReader, request LaunchRequest) (LaunchRecord, error) {
	if p == nil || p.root == nil || request.Validate() != nil {
		return LaunchRecord{}, ErrProtocol
	}
	lock, err := p.root.LockWriter(ctx)
	if err != nil {
		return LaunchRecord{}, err
	}
	defer lock.Close()
	process, err := p.Read(ctx)
	if err != nil {
		return LaunchRecord{}, err
	}
	if process.Current != nil {
		return LaunchRecord{}, ErrConflict
	}
	if err := launchAllowed(ctx, journal, request); err != nil {
		return LaunchRecord{}, err
	}
	prior, err := p.ReadLaunch(ctx)
	if err != nil {
		return LaunchRecord{}, err
	}
	if prior.State == "reserved" || prior.State == "released" {
		return LaunchRecord{}, ErrRecoveryRequired
	}
	if prior.State == "prepared" {
		if prior.Request != nil && *prior.Request == request {
			return prior, nil
		}
		return LaunchRecord{}, ErrConflict
	}
	if prior.Generation == math.MaxUint64 {
		return LaunchRecord{}, ErrRecoveryRequired
	}
	next := LaunchRecord{Schema: 1, Generation: prior.Generation + 1, State: "prepared", Request: &request}
	return p.writeLaunch(ctx, next)
}

// ReserveLaunch is durable BEFORE fork. A lost reply/crash after this point
// cannot start the same launch a second time, even if no PID was recorded.
// The native loop still rereads the journal before exec; the core opens held
// and independently rechecks durable authority before any later admission.
func (p *ProcessRecordFile) ReserveLaunch(ctx context.Context, journal JournalReader, txID, launchID string) (LaunchRecord, error) {
	if p == nil || p.root == nil || !protocolID(txID, "it1_") || !hexValue(launchID, 32) {
		return LaunchRecord{}, ErrProtocol
	}
	lock, err := p.root.LockWriter(ctx)
	if err != nil {
		return LaunchRecord{}, err
	}
	defer lock.Close()
	current, err := p.Read(ctx)
	if err != nil {
		return LaunchRecord{}, err
	}
	if current.Current != nil {
		return LaunchRecord{}, ErrConflict
	}
	prior, err := p.ReadLaunch(ctx)
	if err != nil {
		return LaunchRecord{}, err
	}
	if prior.State != "prepared" || prior.Request == nil || prior.Request.TxID != txID || prior.Request.LaunchID != launchID {
		return LaunchRecord{}, ErrRecoveryRequired
	}
	if err := launchAllowed(ctx, journal, *prior.Request); err != nil {
		return LaunchRecord{}, err
	}
	if prior.Generation == math.MaxUint64 {
		return LaunchRecord{}, ErrRecoveryRequired
	}
	prior.Generation++
	prior.State = "reserved"
	return p.writeLaunch(ctx, prior)
}
func (p *ProcessRecordFile) writeLaunch(ctx context.Context, next LaunchRecord) (LaunchRecord, error) {
	if next.Validate() != nil {
		return LaunchRecord{}, ErrProtocol
	}
	data, err := json.Marshal(next)
	if err != nil || len(data) > MaxMetadataBytes {
		return LaunchRecord{}, ErrProtocol
	}
	outcome, err := p.root.ReplaceStream(ctx, "launch.json", bytes.NewReader(data), MaxMetadataBytes, 0600)
	if err != nil || outcome != securefs.Committed {
		return LaunchRecord{}, errors.Join(ErrRecoveryRequired, err)
	}
	var detached LaunchRecord
	if strictDecode(data, &detached) != nil {
		return LaunchRecord{}, ErrRecoveryRequired
	}
	return detached, nil
}

// Transaction recovery must first observe a CLOSED candidate, including when
// the journal already contains an OC. A separate explicit release prevents the
// OS host from auto-admitting it before the installer's VerifyReady completed.
// A release is not admission authority: the core still performs its journal
// CAS verdict and exact OC comparison on every private Admit.
func (p *ProcessRecordFile) ReleaseLaunch(ctx context.Context, journal JournalReader, txID, launchID, operationalID string) (LaunchRecord, error) {
	if p == nil || p.root == nil || journal == nil || !protocolID(txID, "it1_") || !hexValue(launchID, 32) || !protocolID(operationalID, "oc1_") {
		return LaunchRecord{}, ErrProtocol
	}
	lock, err := p.root.LockWriter(ctx)
	if err != nil {
		return LaunchRecord{}, err
	}
	defer lock.Close()
	before, err := p.ReadLaunch(ctx)
	if err != nil {
		return LaunchRecord{}, err
	}
	if !oneOf(before.State, "reserved", "released") || before.Request == nil || before.Request.Purpose != "transaction" || before.Request.TxID != txID || before.Request.LaunchID != launchID {
		return LaunchRecord{}, ErrRecoveryRequired
	}
	current, err := p.Read(ctx)
	if err != nil {
		return LaunchRecord{}, err
	}
	if current.Current == nil || current.Current.Ready == nil || current.Current.TxID != txID || current.Current.LaunchID != launchID || current.Current.Runtime != before.Request.Runtime {
		return LaunchRecord{}, ErrReadiness
	}
	j, err := journal.ReadJournal(ctx)
	if err != nil {
		return LaunchRecord{}, err
	}
	plan, err := PlanServiceHost(j, nil)
	if err != nil || plan.Action != "start_held" || plan.Runtime == nil || plan.SchemaReceipt == nil || plan.TxID != txID ||
		plan.OperationalCommitID != operationalID || *plan.Runtime != before.Request.Runtime ||
		current.Current.Ready.StoreSchema != plan.SchemaReceipt.Schema || current.Current.Ready.StoreRevision < plan.SchemaReceipt.Revision {
		return LaunchRecord{}, errors.Join(ErrRecoveryRequired, err)
	}
	if before.State == "released" {
		if before.OperationalCommitID != operationalID {
			return LaunchRecord{}, ErrRecoveryRequired
		}
		return before, nil
	}
	if before.Generation == math.MaxUint64 {
		return LaunchRecord{}, ErrRecoveryRequired
	}
	before.Generation++
	before.State = "released"
	before.OperationalCommitID = operationalID
	return p.writeLaunch(ctx, before)
}
