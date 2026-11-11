//go:build linux || darwin

package installtx

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"time"

	"github.com/Divaaaan/tenebra/internal/securefs"
)

// CoreLauncher can execute only a verified protected runtime's fixed core role.
// It has no caller-controlled executable, arguments, working directory or env.
type CoreLauncher struct {
	mu                sync.Mutex
	data, releases    *securefs.Root
	policy            securefs.Policy
	helperSHA, target string
	output            io.Writer
	starting          int
	active            map[*CoreProcess]bool
	ownedRoots        []*securefs.Root
	closed            bool
	closeDone         chan struct{}
	closeErr          error
}

func OpenCoreLauncher(helperSHA string) (*CoreLauncher, error) {
	if os.Geteuid() != 0 {
		return nil, ErrReadiness
	}
	paths, err := SystemPaths(runtime.GOOS)
	if err != nil {
		return nil, err
	}
	target := "darwin-universal"
	if runtime.GOOS == "linux" {
		if runtime.GOARCH != "amd64" {
			return nil, securefs.ErrUnsupported
		}
		target = "linux-amd64"
	}
	policy := securefs.Policy{Mode: securefs.MachinePrivate}
	data, err := securefs.OpenRoot(paths.Data, policy, true)
	if err != nil {
		return nil, err
	}
	releases, err := securefs.OpenRoot(paths.Releases, policy, true)
	if err != nil {
		data.Close()
		return nil, err
	}
	launcher, err := newCoreLauncher(data, releases, policy, helperSHA, target)
	if err != nil {
		releases.Close()
		data.Close()
		return nil, err
	}
	launcher.ownedRoots = []*securefs.Root{releases, data}
	return launcher, nil
}
func newCoreLauncher(data, releases *securefs.Root, policy securefs.Policy, helperSHA, target string) (*CoreLauncher, error) {
	if data == nil || releases == nil || !oneOfPolicy(policy.Mode) || !hexValue(helperSHA, 64) || !oneOf(target, "linux-amd64", "darwin-universal") {
		return nil, ErrProtocol
	}
	return &CoreLauncher{data: data, releases: releases, policy: policy, helperSHA: helperSHA, target: target, output: io.Discard, active: make(map[*CoreProcess]bool)}, nil
}
func (l *CoreLauncher) StartHeld(ctx context.Context, ref RuntimeRef, txID, launchID string) (*CoreProcess, error) {
	if l == nil || !validRuntime(&ref) || ref.HelperSHA256 != l.helperSHA || !protocolID(txID, "it1_") || !hexValue(launchID, 32) {
		return nil, ErrProtocol
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil, securefs.ErrClosed
	}
	if l.starting != 0 || len(l.active) != 0 {
		l.mu.Unlock()
		return nil, securefs.ErrBusy
	}
	l.starting++
	l.mu.Unlock()
	defer func() { l.mu.Lock(); l.starting--; l.mu.Unlock() }()
	root, err := l.releases.OpenDir(ref.ArtifactSHA256, l.policy, false)
	if err != nil {
		return nil, err
	}
	keep := false
	var lease *securefs.PathLease
	defer func() {
		if !keep {
			if lease != nil {
				lease.Close()
			}
			root.Close()
		}
	}()
	lease, err = root.LeaseTree(ctx, securefs.Limits{MaxFiles: MaxSelectedRoles + 1, MaxBytes: MaxExpandedBytes + MaxMetadataBytes}, nil)
	if err != nil {
		return nil, err
	}
	manifestBody, err := lease.ReadFile("payload.json", MaxMetadataBytes)
	if err != nil {
		return nil, err
	}
	manifest, err := DecodeManifest(manifestBody)
	if err != nil {
		return nil, err
	}
	hash, err := artifactHash(manifest)
	if err != nil || hash != ref.ArtifactSHA256 || manifest.Version != ref.Version || manifest.Target != l.target {
		return nil, ErrProtocol
	}
	if err := verifyRuntimeLease(ctx, lease, manifest); err != nil {
		return nil, err
	}
	directory, err := lease.Path()
	if err != nil {
		return nil, err
	}
	workdir, err := l.data.Path()
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	command := exec.Command(filepath.Join(directory, "tenebra-core"), "--socket", "--maintenance-tx="+txID, "--launch-id="+launchID)
	command.Dir = workdir
	command.Env = []string{"PATH=/usr/bin:/bin", "HOME=/var/empty", "LANG=C", "LC_ALL=C"}
	command.Stdout, command.Stderr = l.output, l.output
	if err := command.Start(); err != nil {
		return nil, err
	}
	child := &CoreProcess{command: command, runtime: ref, txID: txID, lease: lease, root: root, owner: l, done: make(chan struct{}), stopDone: make(chan struct{}),
		identity: ProcessIdentity{PID: uint32(command.Process.Pid), LaunchID: launchID}}
	for _, file := range manifest.Files {
		if file.Name == "tenebra-core" {
			child.coreSHA = file.SHA256
		}
	}
	l.mu.Lock()
	l.active[child] = true
	l.mu.Unlock()
	keep = true
	// Exactly ONE goroutine calls Cmd.Wait. Other waiters observe done; no
	// concurrent wait or FindProcess/kill-by-record-PID can cross PID reuse.
	go child.collect()
	token, err := ProcessStartToken(child.identity.PID)
	if err == nil {
		child.identity.StartToken = token
		err = ctx.Err()
	}
	if err != nil {
		stopCtx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		return nil, errors.Join(err, child.Stop(stopCtx))
	}
	return child, nil
}
func (l *CoreLauncher) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	if l.closed {
		done := l.closeDone
		l.mu.Unlock()
		<-done
		l.mu.Lock()
		err := l.closeErr
		l.mu.Unlock()
		return err
	}
	if l.starting != 0 || len(l.active) != 0 {
		l.mu.Unlock()
		return securefs.ErrBusy
	}
	l.closed = true
	l.closeDone = make(chan struct{})
	l.mu.Unlock()
	var err error
	for _, root := range l.ownedRoots {
		err = errors.Join(err, root.Close())
	}
	l.mu.Lock()
	l.closeErr = err
	close(l.closeDone)
	l.mu.Unlock()
	return err
}

type CoreProcess struct {
	command             *exec.Cmd
	runtime             RuntimeRef
	txID, coreSHA       string
	identity            ProcessIdentity
	lease               *securefs.PathLease
	root                *securefs.Root
	owner               *CoreLauncher
	done                chan struct{}
	mu                  sync.Mutex
	waitErr, cleanupErr error
	stopOnce            sync.Once
	stopDone            chan struct{}
	stopErr             error
}

func (p *CoreProcess) Identity() ProcessIdentity { return p.identity }
func (p *CoreProcess) Runtime() RuntimeRef       { return p.runtime }
func (p *CoreProcess) CoreSHA256() string        { return p.coreSHA }
func (p *CoreProcess) Done() <-chan struct{}     { return p.done }
func (p *CoreProcess) collect() {
	waitErr := p.command.Wait()
	cleanupErr := errors.Join(p.lease.Close(), p.root.Close())
	p.owner.mu.Lock()
	delete(p.owner.active, p)
	p.owner.mu.Unlock()
	p.mu.Lock()
	p.waitErr, p.cleanupErr = waitErr, cleanupErr
	close(p.done)
	p.mu.Unlock()
}
func (p *CoreProcess) Wait(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-p.done:
		p.mu.Lock()
		err := errors.Join(p.waitErr, p.cleanupErr)
		p.mu.Unlock()
		return err
	}
}

// Stop signals only this retained, directly spawned os.Process. It never
// reconstructs a process from a metadata PID. Once requested, cleanup continues
// after a caller deadline; success means the child and runtime lease ended.
func (p *CoreProcess) Stop(ctx context.Context) error {
	p.stopOnce.Do(func() { go p.stop() })
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-p.stopDone:
		p.mu.Lock()
		err := p.stopErr
		p.mu.Unlock()
		return err
	}
}
func (p *CoreProcess) stop() {
	finish := func(err error) { p.mu.Lock(); p.stopErr = err; close(p.stopDone); p.mu.Unlock() }
	select {
	case <-p.done:
		p.mu.Lock()
		err := p.cleanupErr
		p.mu.Unlock()
		finish(err)
		return
	default:
	}
	signalErr := p.command.Process.Signal(syscall.SIGTERM)
	if errors.Is(signalErr, os.ErrProcessDone) {
		signalErr = nil
	}
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case <-p.done:
	case <-timer.C:
		killErr := p.command.Process.Kill()
		if killErr != nil && !errors.Is(killErr, os.ErrProcessDone) {
			finish(errors.Join(signalErr, killErr))
			return
		}
		<-p.done
	}
	p.mu.Lock()
	cleanupErr := p.cleanupErr
	p.mu.Unlock()
	finish(errors.Join(signalErr, cleanupErr))
}
