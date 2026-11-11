//go:build linux || darwin

package installtx

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"time"

	"github.com/Divaaaan/tenebra/internal/securefs"
)

// Pinned public release key from the native Tauri updater configuration. Neither
// a request, manifest, package input nor environment may select a vendor key.
const nativeReleasePublicKey = "dW50cnVzdGVkIGNvbW1lbnQ6IG1pbmlzaWduIHB1YmxpYyBrZXk6IDZGNTZCNDcxMUM2Q0IwRkUKUldUK3NHd2NjYlJXYjljMy9lbkVPa0tHQWYzWDNBR3ErVlU5NEFWdkt2SWNZNllBNU1jcWFRamkK"

// The release builder links these into the helper before its final seal.
// Neither native arguments nor the caller's environment can select them.
var nativeBoundPayloadSHA256, nativeBoundArchiveSHA256 string

type nativeHelperRoots struct {
	maintenance, data, releases, backup *securefs.Root
	journal                             *JournalFile
	manager                             *HostedServiceManager
	files                               *ProtectedFS
	store                               *HeldStorePort
}

func (r *nativeHelperRoots) Close() error {
	var err error
	if r.store != nil {
		err = errors.Join(err, r.store.Close())
	}
	if r.files != nil {
		r.files.mu.Lock()
		ids := make([]string, 0, len(r.files.stages))
		for id := range r.files.stages {
			ids = append(ids, id)
		}
		r.files.mu.Unlock()
		for _, id := range ids {
			err = errors.Join(err, r.files.RemoveOwnedStage(context.Background(), id))
		}
	}
	if r.manager != nil {
		err = errors.Join(err, r.manager.Close())
	}
	if r.journal != nil {
		err = errors.Join(err, r.journal.Close())
	}
	for _, root := range []*securefs.Root{r.backup, r.releases, r.data, r.maintenance} {
		if root != nil {
			err = errors.Join(err, root.Close())
		}
	}
	return err
}

func nativeHelperImage(ctx context.Context) (string, bool, error) {
	if os.Geteuid() != 0 {
		return "", false, ErrHelperPrivilege
	}
	paths, err := SystemPaths(runtime.GOOS)
	if err != nil {
		return "", false, err
	}
	executable, err := os.Executable()
	if err != nil {
		return "", false, err
	}
	packaged := runtime.GOOS == "linux" && executable == "/usr/lib/tenebra/tenebra-helper"
	if executable != paths.Helper && !packaged {
		return "", false, ErrUntrustedHelperRole
	}
	// Fixed installed executable roles may have a public root-owned directory.
	// The file must remain protected and match this actual running kernel image.
	policy := securefs.Policy{Mode: securefs.OperatorInput, Principal: "uid:0"}
	root, err := securefs.OpenRoot(filepath.Dir(executable), policy, false)
	if err != nil {
		return "", false, err
	}
	digest, hashErr := root.HashRunningExecutable(ctx, filepath.Base(executable), 64<<20)
	return digest, packaged, errors.Join(hashErr, root.Close())
}

func initializeNativeJournal(ctx context.Context, journal *JournalFile, expectedManager string, allowCreate bool) (Journal, error) {
	lock, err := journal.LockTransaction(ctx)
	if err != nil {
		return Journal{}, err
	}
	defer lock.Close()
	j, err := journal.ReadJournal(ctx)
	if errors.Is(err, fs.ErrNotExist) {
		if !allowCreate {
			return Journal{}, ErrRecoveryRequired
		}
		manager := expectedManager
		if manager == "" {
			manager = "standalone"
		}
		j = Journal{Schema: 1, Generation: 1, Manager: manager}
		if err := journal.ReplaceJournal(ctx, j); err != nil {
			return Journal{}, err
		}
	} else if err != nil {
		return Journal{}, err
	}
	if j.Validate() != nil || expectedManager != "" && j.Manager != expectedManager {
		return Journal{}, ErrConflict
	}
	return j, nil
}

func installedPackageRequest(ctx context.Context) (ApplyRequest, error) {
	if err := ctx.Err(); err != nil {
		return ApplyRequest{}, err
	}
	root, err := securefs.OpenRoot("/usr/lib/tenebra/package-payload", securefs.Policy{Mode: securefs.OperatorInput, Principal: "uid:0"}, false)
	if err != nil {
		return ApplyRequest{}, err
	}
	body, readErr := root.ReadFile("payload.json", MaxMetadataBytes)
	if err := errors.Join(readErr, root.Close()); err != nil {
		return ApplyRequest{}, err
	}
	m, err := DecodeManifest(body)
	if err != nil {
		return ApplyRequest{}, err
	}
	hash, err := artifactHash(m)
	if err != nil {
		return ApplyRequest{}, err
	}
	op, err := newID("so1_")
	if err != nil {
		return ApplyRequest{}, err
	}
	return ApplyRequest{Schema: 1, OperationID: op, Action: "install", Trust: "package_manager", ExpectedVersion: m.Version, ExpectedSourceSHA: m.SourceSHA, ExpectedPayloadSHA256: hash,
		Source: &Source{Layout: "installed_package", AbsolutePath: "/usr/lib/tenebra/package-payload"}}, nil
}

func RunNativeHelperCommand(ctx context.Context, command HelperCommand) (result Result, resultErr error) {
	if command.Name == "status" {
		status, err := ReadPublicStatus()
		return Result{Status: status}, err
	}
	if os.Geteuid() != 0 {
		return Result{}, ErrHelperPrivilege
	}
	if !oneOf(command.Name, "service-host", "apply", "remove", "recover", "stage-package") {
		return Result{}, ErrProtocol
	}
	helperSHA, packaged, err := nativeHelperImage(ctx)
	if err != nil {
		return Result{}, err
	}
	if command.Name == "service-host" {
		host, err := OpenServiceHost(helperSHA)
		if err != nil {
			return Result{}, err
		}
		return Result{}, errors.Join(host.Run(ctx), host.Close())
	}
	paths, err := SystemPaths(runtime.GOOS)
	if err != nil {
		return Result{}, err
	}
	target := "darwin-universal"
	if runtime.GOOS == "linux" {
		if runtime.GOARCH != "amd64" {
			return Result{}, securefs.ErrUnsupported
		}
		target = "linux-amd64"
	}
	roots := &nativeHelperRoots{}
	defer func() { resultErr = errors.Join(resultErr, roots.Close()) }()
	policy := securefs.Policy{Mode: securefs.MachinePrivate}
	roots.maintenance, err = securefs.OpenRoot(paths.Maintenance, policy, true)
	if err != nil {
		return Result{}, err
	}
	roots.journal, err = OpenJournalFile(roots.maintenance)
	if err != nil {
		return Result{}, err
	}
	expectedManager := ""
	if command.Name == "stage-package" {
		expectedManager = command.Manager
	}
	allowCreate := command.Name == "stage-package" || !packaged && (command.Name == "remove" || command.Name == "apply" && command.Request != nil && command.Request.Action == "install")
	j, err := initializeNativeJournal(ctx, roots.journal, expectedManager, allowCreate)
	if err != nil {
		return Result{}, err
	}
	if packaged != (j.Manager != "standalone") {
		return Result{}, ErrConflict
	}
	job, err := newNativeServiceHostJob(roots.journal)
	if err != nil {
		return Result{}, err
	}
	// Unknown/custom jobs fail before staging or OS actions. The exact legacy
	// template is rechecked with its snapshot capability before any OS action.
	if err := job.VerifyKnown(ctx); err != nil && !errors.Is(err, ErrLegacyAdoptionRequired) {
		return Result{}, err
	}
	request := command.Request
	if command.Name == "stage-package" {
		r, err := installedPackageRequest(ctx)
		if err != nil {
			return Result{}, err
		}
		request = &r
	}
	if oneOf(command.Name, "apply", "remove", "stage-package") && request == nil {
		return Result{}, ErrProtocol
	}
	trust := "operator_dev"
	if request != nil && request.Action == "install" {
		trust = request.Trust
		if j.Manager == "standalone" && trust == "package_manager" || j.Manager != "standalone" && trust != "package_manager" {
			return Result{}, ErrConflict
		}
	}
	roots.data, err = securefs.OpenRoot(paths.Data, policy, true)
	if err != nil {
		return Result{}, err
	}
	roots.releases, err = securefs.OpenRoot(paths.Releases, policy, true)
	if err != nil {
		return Result{}, err
	}
	roots.backup, err = roots.maintenance.OpenDir("checkpoints", policy, true)
	if err != nil {
		return Result{}, err
	}
	roots.manager, err = NewHostedServiceManager(roots.maintenance, job)
	if err != nil {
		return Result{}, err
	}
	// Root CLI identity comes from the kernel. No SUDO_UID/JSON/source-owner
	// inference is an authenticated operator. User-owned GUI inputs require the
	// separately authenticated native bootstrap/broker before this root role.
	sourcePolicy := SourcePolicy{Trust: trust, OperatorPrincipal: "uid:" + strconv.Itoa(os.Geteuid()), ReleasePublicKey: nativeReleasePublicKey, HelperSHA256: helperSHA, Target: target, AcknowledgeUntrusted: command.AcknowledgeUntrusted}
	if target == "darwin-universal" && trust == "release" {
		if !hexValue(nativeBoundPayloadSHA256, 64) || !hexValue(nativeBoundArchiveSHA256, 64) {
			return Result{}, ErrSignature
		}
		sourcePolicy.BoundPayloadSHA256, sourcePolicy.BoundArchiveSHA256 = nativeBoundPayloadSHA256, nativeBoundArchiveSHA256
	}
	roots.files, err = NewProtectedFS(roots.releases, roots.journal, sourcePolicy)
	if err != nil {
		return Result{}, err
	}
	if legacy, ok := job.(interface{ configureLegacy(*ProtectedFS) }); ok {
		legacy.configureLegacy(roots.files)
	}
	if err := job.VerifyKnown(ctx); err != nil {
		canSnapshot := j.Manager == "standalone" && j.Active == nil && j.Pending == nil &&
			oneOf(command.Name, "apply", "remove") && request != nil && oneOf(request.Action, "install", "remove")
		if !errors.Is(err, ErrLegacyAdoptionRequired) || !canSnapshot {
			return Result{}, err
		}
	}
	roots.store, err = NewHeldStorePort(roots.data, roots.backup, roots.journal, roots.manager)
	if err != nil {
		return Result{}, err
	}
	probe, err := NewMaintenanceReadyProbe(roots.manager.ready)
	if err != nil {
		return Result{}, err
	}
	installer := New(roots.files, roots.store, roots.manager, probe)
	switch command.Name {
	case "apply", "remove":
		result, resultErr = installer.Apply(ctx, *request)
	case "stage-package":
		result, resultErr = installer.StagePackage(ctx, *request, command.Manager)
	case "recover":
		result, resultErr = installer.RecoverTransaction(ctx, command.TransactionID)
	}
	// Projection failures never grant authority or roll back an already durable
	// operational commit. CLI status remains unavailable until its file is valid.
	if validatePublicStatus(result.Status) == nil {
		publication, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		_ = PublishPublicStatus(publication, result.Status)
		cancel()
	}
	return result, resultErr
}
