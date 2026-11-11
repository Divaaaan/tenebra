package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"

	"github.com/Divaaaan/tenebra/core/control"
	"github.com/Divaaaan/tenebra/core/state"
	"github.com/Divaaaan/tenebra/internal/securefs"
)

var errRecoveryRequired = errors.New("offline recovery and explicit acknowledgement required")

type daemonFilesystem struct {
	State, Private *securefs.Root
	Development    bool
	closers        []io.Closer
	privatePolicy  securefs.Policy
	managed        *managedBoot
	canonical      *state.Store
}

func (f daemonFilesystem) Close() error {
	var result error
	for i := len(f.closers) - 1; i >= 0; i-- {
		result = errors.Join(result, f.closers[i].Close())
	}
	return result
}

type daemonBootstrap struct {
	Open  func(context.Context) (daemonFilesystem, error)
	Build func(*daemonFilesystem) (*control.Daemon, error)
}

func (b daemonBootstrap) Prepare(ctx context.Context) (*control.Daemon, daemonFilesystem, error) {
	if b.Open == nil || b.Build == nil {
		return nil, daemonFilesystem{}, securefs.ErrUnsupported
	}
	if err := ctx.Err(); err != nil {
		return nil, daemonFilesystem{}, err
	}
	filesystem, err := b.Open(ctx)
	if err != nil {
		return nil, daemonFilesystem{}, err
	}
	if filesystem.State == nil || filesystem.Private == nil {
		return nil, daemonFilesystem{}, errors.Join(securefs.ErrUntrusted, filesystem.Close())
	}
	if err := ctx.Err(); err != nil {
		return nil, daemonFilesystem{}, errors.Join(err, filesystem.Close())
	}
	daemon, err := b.Build(&filesystem)
	if err != nil {
		return nil, daemonFilesystem{}, errors.Join(err, filesystem.Close())
	}
	if daemon == nil {
		return nil, daemonFilesystem{}, errors.Join(securefs.ErrUntrusted, filesystem.Close())
	}
	return daemon, filesystem, err
}

// The caller owns anchor. All payload consumers run only after this metadata
// gate has accepted the complete active tree and every durable recovery fence.
func openMachineLayout(ctx context.Context, anchor *securefs.Root, privatePolicy, containerPolicy securefs.Policy) (result daemonFilesystem, failure error) {
	success := false
	defer func() {
		if !success {
			failure = errors.Join(failure, result.Close())
			result = daemonFilesystem{}
		}
	}()
	if err := ctx.Err(); err != nil {
		return result, err
	}
	private, err := anchor.OpenDir("Tenebra-Private", privatePolicy, true)
	if err != nil {
		// An unsafe private anchor is not repaired or relocated to temp.
		return result, fmt.Errorf("private machine anchor: %w", err)
	}
	result.Private = private
	result.privatePolicy = privatePolicy
	result.closers = append(result.closers, private)
	vault, err := private.OpenDir("forensics", privatePolicy, true)
	if err != nil {
		return result, err
	}
	result.closers = append(result.closers, vault)
	if err := checkRecoveryFence(vault); err != nil {
		return result, err
	}
	// A planted log must not be opened or discovered only after state readers
	// have run. Forensic originals are intentionally outside this active tree.
	logs, err := private.OpenDir("logs", privatePolicy, true)
	if err != nil {
		return result, err
	}
	logLease, err := logs.LeaseTree(ctx, securefs.Limits{MaxFiles: 16, MaxBytes: 32 << 20}, nil)
	if err != nil {
		return result, errors.Join(err, logs.Close())
	}
	if err := errors.Join(logLease.Close(), logs.Close()); err != nil {
		return result, err
	}
	quarantine := func(parent *securefs.Root, name string) error {
		receipt, err := parent.Quarantine(name, vault)
		// Even a partially completed quarantine is a recovery-required result.
		// Only an opaque ID may appear in diagnostics; never foreign payloads.
		if receipt.ID != "" {
			return fmt.Errorf("%w (receipt %s)", errRecoveryRequired, receipt.ID)
		}
		if err != nil {
			return errors.Join(errRecoveryRequired, err)
		}
		return errRecoveryRequired
	}
	container, err := anchor.OpenDir("Tenebra", containerPolicy, false)
	if errors.Is(err, fs.ErrNotExist) {
		container, err = anchor.OpenDir("Tenebra", containerPolicy, true)
	} else if errors.Is(err, securefs.ErrUntrusted) {
		return result, quarantine(anchor, "Tenebra")
	}
	if err != nil {
		return result, err
	}
	result.closers = append(result.closers, container)
	state, err := container.OpenDir("data", privatePolicy, false)
	if errors.Is(err, fs.ErrNotExist) {
		state, err = container.OpenDir("data", privatePolicy, true)
	} else if errors.Is(err, securefs.ErrUntrusted) {
		return result, quarantine(container, "data")
	}
	if err != nil {
		return result, err
	}
	result.State = state
	result.closers = append(result.closers, state)
	lease, err := state.LeaseTree(ctx, securefs.Limits{MaxFiles: 4096, MaxBytes: 1 << 30}, nil)
	if err != nil {
		var badPath *fs.PathError
		if errors.Is(err, securefs.ErrUntrusted) && errors.As(err, &badPath) {
			// A trusted container with one unsafe entry retains its good
			// siblings. An unsafe directory is moved whole, never mined for
			// apparently trusted-looking descendants.
			name := strings.SplitN(badPath.Path, "/", 2)[0]
			return result, quarantine(state, name)
		}
		return result, err
	}
	if err := lease.Close(); err != nil {
		return result, err
	}
	// The canonical repository acquires the sole writer in Build. This metadata
	// gate must not retain a competing second lock.
	success = true
	return result, nil
}

func strictRecoveryJSON(data []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return errRecoveryRequired
	}
	if decoder.Decode(new(any)) != io.EOF {
		return errRecoveryRequired
	}
	return nil
}

func checkRecoveryFence(vault *securefs.Root) error {
	data, err := vault.ReadFile("quarantine-index.json", 64<<10)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return errRecoveryRequired
	}
	var index struct {
		Version int      `json:"version"`
		IDs     []string `json:"ids"`
	}
	if strictRecoveryJSON(data, &index) != nil || index.Version != 1 || index.IDs == nil || len(index.IDs) > 1024 {
		return errRecoveryRequired
	}
	seen := make(map[string]bool, len(index.IDs))
	for _, id := range index.IDs {
		if len(id) != 34 || !strings.HasPrefix(id, "q_") || strings.ToLower(id) != id || seen[id] {
			return errRecoveryRequired
		}
		if decoded, err := hex.DecodeString(id[2:]); err != nil || len(decoded) != 16 {
			return errRecoveryRequired
		}
		seen[id] = true
		data, err := vault.ReadFile("quarantine-"+id+".json", 16<<10)
		if err != nil {
			return errRecoveryRequired
		}
		var record struct {
			Version        int    `json:"version"`
			ID             string `json:"id"`
			State          string `json:"state"`
			Acknowledged   bool   `json:"acknowledged"`
			SourceName     string `json:"source_name"`
			SourceObjectID string `json:"source_object_id"`
			SourceOwner    string `json:"source_owner"`
			OriginalBucket string `json:"original_bucket"`
			RecoveryCopy   string `json:"recovery_copy,omitempty"`
			ArchiveSHA256  string `json:"archive_sha256,omitempty"`
			Trust          string `json:"trust"`
		}
		if strictRecoveryJSON(data, &record) != nil || record.Version != 1 || record.ID != id ||
			!record.Acknowledged || record.State != "recovery_required" || record.Trust != "forensic_untrusted" ||
			record.RecoveryCopy != "recovery-"+id || record.OriginalBucket != "quarantine-original-"+id || len(record.ArchiveSHA256) != 64 {
			return fmt.Errorf("%w (receipt %s)", errRecoveryRequired, id)
		}
	}
	return nil
}
