//go:build linux || darwin

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/Divaaaan/tenebra/core/buildinfo"
	"github.com/Divaaaan/tenebra/core/control"
	"github.com/Divaaaan/tenebra/core/state"
	"github.com/Divaaaan/tenebra/internal/installtx"
	"github.com/Divaaaan/tenebra/internal/securefs"
)

func unixServiceRoot() bool { return os.Geteuid() == 0 }
func openManagedFilesystem(ctx context.Context, authority runAuthority) (daemonFilesystem, error) {
	if !unixServiceRoot() {
		return daemonFilesystem{}, installtx.ErrProtocol
	}
	paths, err := installtx.SystemPaths(runtime.GOOS)
	if err != nil {
		return daemonFilesystem{}, err
	}
	executable, err := os.Executable()
	if err != nil {
		return daemonFilesystem{}, err
	}
	return openManagedFilesystemAt(ctx, paths, authority, executable)
}
func openManagedFilesystemAt(ctx context.Context, paths installtx.PlatformPaths, authority runAuthority, executable string) (result daemonFilesystem, failure error) {
	if !authority.managed || !opaqueLaunch(authority.transaction, "it1_") || !opaqueLaunch(authority.launch, "") {
		return result, installtx.ErrProtocol
	}
	keep := false
	defer func() {
		if !keep {
			failure = errors.Join(failure, result.Close())
			result = daemonFilesystem{}
		}
	}()
	policy := securefs.Policy{Mode: securefs.MachinePrivate}
	private, err := securefs.OpenRoot(paths.Maintenance, policy, false)
	if err != nil {
		return result, err
	}
	result.Private = private
	result.privatePolicy = policy
	result.closers = append(result.closers, private)
	journal, err := installtx.OpenJournalFile(private)
	if err != nil {
		return result, err
	}
	result.closers = append(result.closers, journal)
	j, err := journal.ReadJournal(ctx)
	if err != nil || j.Validate() != nil || j.Transaction == nil {
		return result, errors.Join(installtx.ErrRecoveryRequired, err)
	}
	tx := j.Transaction
	if tx.ID != authority.transaction || tx.Candidate == nil || tx.SchemaReceipt == nil ||
		!(tx.Phase == "schema_ready" || tx.Phase == "ready" || tx.Phase == "committed_closed" || tx.Phase == "committed_open") {
		return result, installtx.ErrRecoveryRequired
	}
	releases, err := securefs.OpenRoot(paths.Releases, policy, false)
	if err != nil {
		return result, err
	}
	result.closers = append(result.closers, releases)
	target := "darwin-universal"
	if runtime.GOOS == "linux" {
		if runtime.GOARCH != "amd64" {
			return result, securefs.ErrUnsupported
		}
		target = "linux-amd64"
	}
	manifest, err := installtx.InspectRuntime(ctx, releases, *tx.Candidate, target)
	if err != nil || manifest.Version != buildinfo.Version {
		return result, errors.Join(installtx.ErrReadiness, err)
	}
	runtimeRoot, err := releases.OpenDir(tx.Candidate.ArtifactSHA256, policy, false)
	if err != nil {
		return result, err
	}
	result.closers = append(result.closers, runtimeRoot)
	lease, err := runtimeRoot.LeaseTree(ctx, securefs.Limits{MaxFiles: 33, MaxBytes: (2 << 30) + (64 << 10)}, nil)
	if err != nil {
		return result, err
	}
	result.closers = append(result.closers, lease)
	directory, err := lease.Path()
	if err != nil {
		return result, err
	}
	if filepath.Clean(executable) != filepath.Join(directory, "tenebra-core") {
		return result, installtx.ErrReadiness
	}
	data, err := securefs.OpenRoot(paths.Data, policy, false)
	if err != nil {
		return result, err
	}
	result.State = data
	result.closers = append(result.closers, data)
	// A held new core may not invent another migration before readiness. The
	// installer's receipt must already describe this canonical lineage.
	body, err := data.ReadFile("profiles.json", 64<<20)
	if err != nil {
		return result, err
	}
	var snapshot state.Snapshot
	if json.Unmarshal(body, &snapshot) != nil || snapshot.Validate() != nil {
		return result, installtx.ErrRecoveryRequired
	}
	sum := sha256.Sum256(body)
	observed := state.SchemaReceipt{Schema: uint32(snapshot.SchemaVersion), Revision: snapshot.Revision, MigrationID: snapshot.MigrationID, CanonicalSHA256: hex.EncodeToString(sum[:])}
	if tx.OperationalCommitID == "" {
		if observed != *tx.SchemaReceipt {
			return result, installtx.ErrRecoveryRequired
		}
	} else if observed.Schema != tx.SchemaReceipt.Schema || observed.MigrationID != tx.SchemaReceipt.MigrationID || observed.Revision < tx.SchemaReceipt.Revision ||
		observed.Revision == tx.SchemaReceipt.Revision && observed.CanonicalSHA256 != tx.SchemaReceipt.CanonicalSHA256 {
		return result, installtx.ErrRecoveryRequired
	}
	ready, err := private.OpenDir("ready", policy, true)
	if err != nil {
		return result, err
	}
	result.closers = append(result.closers, ready)
	socketOwners, err := private.OpenDir("socket-owner", policy, true)
	if err != nil {
		return result, err
	}
	result.closers = append(result.closers, socketOwners)
	result.managed = &managedBoot{authority: authority, paths: paths, journal: journal, runtime: *tx.Candidate, manifest: manifest, runtimeRoot: runtimeRoot, readyRoot: ready, socketOwners: socketOwners}
	result.Development = false
	keep = true
	return result, nil
}

func startManagedPrivate(ctx context.Context, daemon *control.Daemon, filesystem *daemonFilesystem) (func(), error) {
	if filesystem == nil || filesystem.managed == nil || filesystem.canonical == nil {
		return nil, installtx.ErrProtocol
	}
	boot := filesystem.managed
	listener, err := installtx.ListenPrivateReady(ctx, boot.readyRoot, boot.socketOwners, boot.authority.transaction, boot.authority.launch)
	if err != nil {
		return nil, err
	}
	one, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	var startup sync.Once
	coreSHA := ""
	for _, file := range boot.manifest.Files {
		if file.Name == "tenebra-core" {
			coreSHA = file.SHA256
		}
	}
	endpoint := installtx.PrivateEndpoint{TxID: boot.authority.transaction,
		Info: func(ctx context.Context) (installtx.ReadyReply, error) {
			view := filesystem.canonical.View()
			admission := "closed"
			if err := daemon.BusinessReady(); err == nil {
				admission = "open"
			} else if !errors.Is(err, state.ErrMaintenancePending) {
				return installtx.ReadyReply{}, err
			}
			return installtx.ReadyReply{Schema: 1, StoreSchema: uint32(view.SchemaVersion), Status: "ready", TxID: boot.authority.transaction,
				DaemonVersion: buildinfo.Version, DaemonInstanceID: daemon.InstanceID(), LaunchID: boot.authority.launch, CoreSHA256: coreSHA,
				Transport: "unix", BusinessAdmission: admission, StoreRevision: view.Revision}, nil
		},
		Admit: func(ctx context.Context, txID, ocID string) error {
			ordinaryBoot := false
			err := boot.journal.WithDurableAdmission(ctx, txID, ocID, boot.runtime, func(j installtx.Journal) error {
				if daemon.MaintenanceFenceStarted() {
					return state.ErrMaintenancePending
				}
				view := filesystem.canonical.View()
				receipt := j.Transaction.SchemaReceipt
				if view.SchemaVersion != int(receipt.Schema) || view.MigrationID != receipt.MigrationID || view.Revision < receipt.Revision {
					return installtx.ErrRecoveryRequired
				}
				ordinaryBoot = j.Transaction.Phase == "committed_open"
				return filesystem.canonical.Admit()
			})
			if err != nil {
				return err
			}
			startup.Do(func() {
				if boot.startup != nil {
					boot.startup(daemon, ordinaryBoot)
				} else {
					startProductionConnectionWithIntent(daemon, ordinaryBoot)
				}
			})
			return nil
		},
		Fence: func(ctx context.Context, txID string) error {
			if err := boot.journal.WithDurableFence(ctx, txID, boot.runtime, func() error {
				daemon.BeginMaintenanceFence()
				return nil
			}); err != nil {
				return err
			}
			return daemon.FenceForMaintenance(ctx)
		},
	}
	go func() { done <- installtx.ServePrivate(one, listener, endpoint) }()
	var closeOnce sync.Once
	close := func() { closeOnce.Do(func() { stop(); listener.Close(); <-done }) }
	return close, nil
}
func serveManagedSocket(ctx context.Context, daemon *control.Daemon, path string) error {
	listener, err := control.ListenSocket(path)
	if err != nil {
		return err
	}
	return control.ServeListener(ctx, daemon, listener)
}

var _ io.Closer = (*installtx.JournalFile)(nil)
