//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows/svc"

	"github.com/Divaaaan/tenebra/core/control"
)

// serviceName is the name the installer registers the core under with the
// service control manager.
const serviceName = "tenebra"

// maybeRunService detects whether the process was started by the Windows
// service control manager and, if so, runs the core as that service until it
// is stopped. handled reports whether the service path ran (err is then its
// outcome); handled=false with a nil err means "not a service, continue as a
// console process".
func maybeRunService() (handled bool, err error) {
	isSvc, err := svc.IsWindowsService()
	if err != nil {
		return false, fmt.Errorf("detect service environment: %w", err)
	}
	if !isSvc {
		return false, nil
	}
	log.SetFlags(log.LstdFlags)
	if err := svc.Run(serviceName, coreService{}); err != nil {
		return true, fmt.Errorf("run service: %w", err)
	}
	return true, nil
}

// coreService adapts the core to the SCM handler contract: starting brings up
// the daemon and the named-pipe listener, Stop/Shutdown tears a live tunnel
// down before the service reports stopped.
type coreService struct{}

func (coreService) Execute(args []string, req <-chan svc.ChangeRequest, status chan<- svc.Status) (svcSpecificEC bool, exitCode uint32) {
	status <- svc.Status{State: svc.StartPending}

	if err := enableServiceProcessQuery(); err != nil {
		log.Printf("fatal: service process authentication: %v", err)
		return false, 1
	}
	bootstrap := daemonBootstrap{Open: openServiceFilesystem, Build: buildDaemonWithFilesystem}
	daemon, filesystem, err := bootstrap.Prepare(context.Background())
	if err != nil {
		log.Printf("fatal: service filesystem: %v", err)
		return false, 1
	}
	defer filesystem.Close()
	defer daemon.Close()
	serviceLog, err := openServiceLog(filesystem)
	if err != nil {
		log.Printf("fatal: private service log: %v", err)
		return false, 1
	}
	defer serviceLog.Close()
	log.SetOutput(serviceLog)
	defer log.SetOutput(os.Stderr)
	fileLogTail = serviceLog.Tail
	defer func() { fileLogTail = nil }()
	startProductionConnection(daemon)
	l, err := control.ListenPipe(control.PipeName)
	if err != nil {
		log.Printf("fatal: %v", err)
		return false, 1
	}
	log.Printf("tenebra-core: serving the control protocol on %s", control.PipeName)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	served := make(chan error, 1)
	go func() { served <- control.ServeListener(ctx, daemon, l) }()
	// The same background work a console-started core does. Missing here, it was
	// missing on every ordinary Windows install: see startBackgroundJobs.
	startBackgroundJobs(ctx, daemon)

	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown | svc.AcceptSessionChange}

	for {
		select {
		case err := <-served:
			// The listener died out from under us; report a failed service
			// rather than a clean stop so the SCM's recovery actions apply.
			log.Printf("fatal: %v", err)
			status <- svc.Status{State: svc.StopPending}
			return false, 1
		case c := <-req:
			switch c.Cmd {
			case svc.Interrogate:
				status <- c.CurrentStatus
			case svc.SessionChange:
				go daemon.ReconcileSystemProxyWhenIdle()
			case svc.Stop, svc.Shutdown:
				// The teardown stops sing-box and waits for the connection
				// goroutines to drain; give the SCM an explicit budget for that
				// so it doesn't declare the service hung.
				status <- svc.Status{State: svc.StopPending, WaitHint: 10_000}
				cancel()
				err := <-served
				log.Printf("tenebra-core: service stopped")
				if err != nil && !errors.Is(err, context.Canceled) {
					return false, 1
				}
				return false, 0
			}
		}
	}
}

// findBundledSingbox resolves the sing-box binary from the installed layout
// around the core executable: the Tauri bundle installs tenebra-core.exe into
// the install root with the bundled resources in resources\ next to it, while
// a flat layout (a dev checkout, a hand-rolled install) keeps sing-box beside
// the executable. Returns "" when neither location has it.
func findBundledSingbox(exeDir string) string {
	for _, p := range []string{
		filepath.Join(exeDir, "resources", "sing-box.exe"),
		filepath.Join(exeDir, "sing-box.exe"),
	} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// servePipe serves the control protocol on the tenebra named pipe from a
// console process — the development counterpart of the service transport, so
// the pipe path can be exercised without installing a service.
func servePipe(ctx context.Context, d *control.Daemon) error {
	l, err := control.ListenPipe(control.PipeName)
	if err != nil {
		return err
	}
	log.Printf("tenebra-core: serving the control protocol on %s", control.PipeName)
	return control.ServeListener(ctx, d, l)
}
