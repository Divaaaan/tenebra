//go:build linux || darwin

package installtx

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"

	"github.com/Divaaaan/tenebra/internal/securefs"
)

// Only the isolated copied TEST executable calls this fake-store endpoint.
// Kernel peers, protected journal authority, socket ownership and actual child
// lifetimes are real; production C3 is covered by the independent main fixture.
func startHostFixturePrivate(base, release, tx, launch string, listener net.Listener) (func(), error) {
	maintenance, err := securefs.OpenRoot(filepath.Join(base, "maintenance"), securefs.Policy{Mode: securefs.MachinePrivate}, false)
	if err != nil {
		return nil, err
	}
	journal, err := OpenJournalFile(maintenance)
	if err != nil {
		maintenance.Close()
		return nil, err
	}
	closeRoots := func() { journal.Close(); maintenance.Close() }
	j, err := journal.ReadJournal(context.Background())
	if err != nil || j.Transaction == nil || j.Transaction.ID != tx || j.Transaction.Candidate == nil || j.Transaction.SchemaReceipt == nil {
		closeRoots()
		return nil, ErrReadiness
	}
	ref, receipt := *j.Transaction.Candidate, *j.Transaction.SchemaReceipt
	core, err := os.ReadFile(filepath.Join(release, "tenebra-core"))
	if err != nil {
		closeRoots()
		return nil, err
	}
	sum := sha256.Sum256(core)
	instance, err := newID("")
	if err != nil {
		closeRoots()
		return nil, err
	}
	var open, fenced atomic.Bool
	var revision atomic.Uint64
	revision.Store(receipt.Revision)
	endpoint := PrivateEndpoint{TxID: tx,
		Info: func(context.Context) (ReadyReply, error) {
			admission := "closed"
			if open.Load() {
				admission = "open"
			}
			return ReadyReply{Schema: 1, StoreSchema: receipt.Schema, StoreRevision: revision.Load(), DaemonVersion: ref.Version, DaemonInstanceID: instance,
				LaunchID: launch, CoreSHA256: hex.EncodeToString(sum[:]), Transport: "unix", BusinessAdmission: admission}, nil
		},
		Admit: func(ctx context.Context, id, oc string) error {
			return journal.WithDurableAdmission(ctx, id, oc, ref, func(Journal) error {
				if fenced.Load() {
					return ErrRecoveryRequired
				}
				if !open.Swap(true) {
					revision.Add(9)
				}
				return nil
			})
		},
		Fence: func(ctx context.Context, id string) error {
			return journal.WithDurableFence(ctx, id, ref, func() error { fenced.Store(true); open.Store(false); return nil })
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- ServePrivate(ctx, listener, endpoint) }()
	return func() {
		cancel()
		listener.Close()
		<-done
		closeRoots()
	}, nil
}
