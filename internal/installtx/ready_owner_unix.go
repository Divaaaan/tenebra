//go:build linux || darwin

package installtx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"math"
	"os"

	"github.com/Divaaaan/tenebra/internal/securefs"
	"golang.org/x/sys/unix"
)

type ReadySocketOwner struct {
	TxID       string                  `json:"tx_id"`
	PID        uint32                  `json:"pid"`
	StartToken string                  `json:"start_token"`
	LaunchID   string                  `json:"launch_id"`
	UID        uint32                  `json:"uid"`
	Socket     securefs.SocketIdentity `json:"socket"`
}

func (o ReadySocketOwner) Process() ProcessIdentity {
	return ProcessIdentity{PID: o.PID, StartToken: o.StartToken, LaunchID: o.LaunchID}
}
func (o ReadySocketOwner) Validate() error {
	if o.UID != 0 || !protocolID(o.TxID, "it1_") || !validHostIdentity(o.Process()) || o.Socket.Validate() != nil {
		return ErrProtocol
	}
	return nil
}

type ReadySocketRecord struct {
	Schema     uint32            `json:"schema"`
	Generation uint64            `json:"generation"`
	Owner      *ReadySocketOwner `json:"owner"`
}

func (r ReadySocketRecord) Validate() error {
	if r.Schema != 1 || r.Generation == 0 || r.Owner == nil || r.Owner.Validate() != nil {
		return ErrProtocol
	}
	return nil
}
func ReadReadySocketOwner(ctx context.Context, owners *securefs.Root) (ReadySocketRecord, error) {
	if owners == nil {
		return ReadySocketRecord{}, ErrProtocol
	}
	if err := ctx.Err(); err != nil {
		return ReadySocketRecord{}, err
	}
	body, err := owners.ReadFile("socket-owner.json", MaxMetadataBytes)
	if errors.Is(err, fs.ErrNotExist) {
		return ReadySocketRecord{Schema: 1}, nil
	}
	if err != nil {
		return ReadySocketRecord{}, err
	}
	var record ReadySocketRecord
	if strictDecode(body, &record) != nil || record.Validate() != nil {
		return ReadySocketRecord{}, ErrRecoveryRequired
	}
	return record, nil
}

// This is only an ended-LIFETIME check, never permission to signal a record PID.
// Unknown/permission/parser errors stay unknown; a zero-signal ESRCH or an
// observed different kernel start token proves that the old lifetime ended.
func ProcessDefinitelyEnded(ctx context.Context, expected ProcessIdentity) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !validHostIdentity(expected) {
		return ErrReadiness
	}
	token, err := ProcessStartToken(expected.PID)
	if err == nil {
		if token == expected.StartToken {
			return ErrNotReady
		}
		return nil
	}
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, unix.ESRCH) {
		return nil
	}
	if probeErr := unix.Kill(int(expected.PID), 0); errors.Is(probeErr, unix.ESRCH) {
		return nil
	}
	return errors.Join(ErrRecoveryRequired, err)
}
func cleanupReadyOwner(ctx context.Context, ready *securefs.Root, owner ReadySocketOwner) error {
	if ready == nil || owner.Validate() != nil {
		return ErrProtocol
	}
	verify := func(ctx context.Context) error { return ProcessDefinitelyEnded(ctx, owner.Process()) }
	return ready.RemoveVerifiedSocketPair(ctx, owner.TxID+".sock", "tmp_"+owner.LaunchID+".sock", owner.Socket, verify)
}
func CleanupPrivateReady(ctx context.Context, ready, owners *securefs.Root) error {
	if os.Geteuid() != 0 || ready == nil || owners == nil {
		return ErrReadiness
	}
	record, err := ReadReadySocketOwner(ctx, owners)
	if err != nil {
		return err
	}
	if record.Owner == nil {
		return nil
	} // no known owner: no unknown node is touched
	return cleanupReadyOwner(ctx, ready, *record.Owner)
}
func sameReadyOwner(a, b ReadySocketRecord) bool {
	if a.Schema != b.Schema || a.Generation != b.Generation {
		return false
	}
	if a.Owner == nil || b.Owner == nil {
		return a.Owner == b.Owner
	}
	return *a.Owner == *b.Owner
}

// The fixed public-to-root ready name is published only AFTER its exact socket
// identity and actual kernel owner are durable. A single bounded owner slot is
// replaced only after the prior owner ended and its exact known nodes cleared.
func ListenPrivateReady(ctx context.Context, ready, owners *securefs.Root, txID, launchID string) (*securefs.SocketLease, error) {
	if os.Geteuid() != 0 || ready == nil || owners == nil || !protocolID(txID, "it1_") || !hexValue(launchID, 32) {
		return nil, ErrProtocol
	}
	prior, err := ReadReadySocketOwner(ctx, owners)
	if err != nil {
		return nil, err
	}
	if prior.Owner != nil {
		if prior.Owner.LaunchID == launchID {
			return nil, ErrRecoveryRequired
		}
		// Process inspection and cleanup verifier are outside owner metadata
		// and lifecycle locks. The following CAS rejects any changed owner.
		if err := cleanupReadyOwner(ctx, ready, *prior.Owner); err != nil {
			return nil, err
		}
	}
	pid := uint32(os.Getpid())
	token, err := ProcessStartToken(pid)
	if err != nil {
		return nil, err
	}
	var socket *securefs.SocketLease
	err = func() error {
		lock, err := owners.LockWriter(ctx)
		if err != nil {
			return err
		}
		defer lock.Close()
		observed, err := ReadReadySocketOwner(ctx, owners)
		if err != nil {
			return err
		}
		if !sameReadyOwner(prior, observed) {
			return ErrConflict
		}
		if prior.Generation == math.MaxUint64 {
			return ErrRecoveryRequired
		}
		socket, err = ready.BindPrivateSocket(ctx, "tmp_"+launchID+".sock")
		if err != nil {
			return err
		}
		next := ReadySocketRecord{Schema: 1, Generation: prior.Generation + 1, Owner: &ReadySocketOwner{
			TxID: txID, PID: pid, StartToken: token, LaunchID: launchID, UID: uint32(os.Geteuid()), Socket: socket.Identity()}}
		if next.Validate() != nil {
			return ErrProtocol
		}
		data, err := json.Marshal(next)
		if err != nil || len(data) > MaxMetadataBytes {
			return ErrProtocol
		}
		outcome, err := owners.ReplaceStream(ctx, "socket-owner.json", bytes.NewReader(data), MaxMetadataBytes, 0600)
		if err != nil || outcome != securefs.Committed {
			return errors.Join(ErrRecoveryRequired, err)
		}
		return nil
	}()
	if err != nil {
		if socket != nil {
			err = errors.Join(err, socket.Close())
		}
		return nil, err
	}
	outcome, err := socket.Publish(ctx, txID+".sock")
	if err != nil || outcome != securefs.Committed {
		return nil, errors.Join(ErrRecoveryRequired, err, socket.Close())
	}
	return socket, nil
}
