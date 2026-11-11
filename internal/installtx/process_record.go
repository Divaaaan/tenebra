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

type ProcessRecord struct {
	Schema     uint32         `json:"schema"`
	Generation uint64         `json:"generation"`
	Current    *HostedProcess `json:"current"`
}
type ProcessRecordFile struct{ root *securefs.Root }

func OpenProcessRecordFile(maintenance *securefs.Root) (*ProcessRecordFile, error) {
	return openProcessRecordFile(maintenance, securefs.Policy{Mode: securefs.MachinePrivate})
}
func openProcessRecordFile(maintenance *securefs.Root, policy securefs.Policy) (*ProcessRecordFile, error) {
	if maintenance == nil || !oneOfPolicy(policy.Mode) {
		return nil, ErrProtocol
	}
	root, err := maintenance.OpenDir("process", policy, true)
	if err != nil {
		return nil, err
	}
	return &ProcessRecordFile{root: root}, nil
}
func (p *ProcessRecordFile) Close() error {
	if p == nil || p.root == nil {
		return nil
	}
	return p.root.Close()
}
func (r ProcessRecord) Validate() error {
	if r.Schema != 1 || r.Generation == 0 || r.Current != nil && r.Current.Validate() != nil {
		return ErrProtocol
	}
	return nil
}
func (p *ProcessRecordFile) Read(ctx context.Context) (ProcessRecord, error) {
	if p == nil || p.root == nil {
		return ProcessRecord{}, ErrProtocol
	}
	if err := ctx.Err(); err != nil {
		return ProcessRecord{}, err
	}
	data, err := p.root.ReadAtomicFile(ctx, "current.json", MaxMetadataBytes)
	if errors.Is(err, fs.ErrNotExist) {
		return ProcessRecord{Schema: 1}, nil
	}
	if err != nil {
		return ProcessRecord{}, err
	}
	var record ProcessRecord
	if strictDecode(data, &record) != nil || record.Validate() != nil {
		return ProcessRecord{}, ErrRecoveryRequired
	}
	return record, nil
}

// Replace uses a separate short CAS writer. It never waits for a child process,
// joins network work or acquires the install transaction/canonical writer lock.
func (p *ProcessRecordFile) Replace(ctx context.Context, generation uint64, current *HostedProcess) (ProcessRecord, error) {
	if p == nil || p.root == nil || generation == math.MaxUint64 || current != nil && current.Validate() != nil {
		return ProcessRecord{}, ErrProtocol
	}
	lock, err := p.root.LockWriter(ctx)
	if err != nil {
		return ProcessRecord{}, err
	}
	defer lock.Close()
	before, err := p.Read(ctx)
	if err != nil {
		return ProcessRecord{}, err
	}
	if before.Generation != generation {
		return ProcessRecord{}, ErrConflict
	}
	next := ProcessRecord{Schema: 1, Generation: generation + 1, Current: current}
	return p.writeProcess(ctx, next)
}

// Caller holds the short process metadata writer, never the host lifecycle lock.
func (p *ProcessRecordFile) writeProcess(ctx context.Context, next ProcessRecord) (ProcessRecord, error) {
	if next.Validate() != nil {
		return ProcessRecord{}, ErrProtocol
	}
	data, err := json.Marshal(next)
	if err != nil || len(data) > MaxMetadataBytes {
		return ProcessRecord{}, ErrProtocol
	}
	state, err := p.root.ReplaceStream(ctx, "current.json", bytes.NewReader(data), MaxMetadataBytes, 0600)
	if err != nil || state != securefs.Committed {
		return ProcessRecord{}, errors.Join(ErrRecoveryRequired, err)
	}
	// Return a detached copy; caller mutation cannot change the persisted
	// process authority metadata or another reader's observed receipt.
	var detached ProcessRecord
	if strictDecode(data, &detached) != nil {
		return ProcessRecord{}, ErrRecoveryRequired
	}
	return detached, nil
}
