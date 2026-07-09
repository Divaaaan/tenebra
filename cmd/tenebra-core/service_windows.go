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
	// A service has no console: stderr goes nowhere, so put the log in a file
	// before anything else writes one. A failure to open it is non-fatal — the
	// service still runs, just silently.
	if f, ferr := openServiceLog(); ferr == nil {
		defer f.Close()
		log.SetOutput(f)
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

	daemon, err := buildDaemon()
	if err != nil {
		log.Printf("fatal: %v", err)
		return false, 1
	}
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

	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}

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

// openServiceLog opens the append-only log file used in service mode, creating
// its directory if needed. It lives under %ProgramData%\Tenebra — a machine
// path, since the service runs as no interactive user — with the temp dir as a
// last resort. Append, not truncate, so a crash loop can't wipe the tail that
// explains it; the separator keeps successive sessions legible, mirroring the
// sidecar log the desktop shell writes.
func openServiceLog() (*os.File, error) {
	base := os.Getenv("ProgramData")
	if base == "" {
		base = os.TempDir()
	}
	dir := filepath.Join(base, "Tenebra")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "service.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	fmt.Fprintln(f, "\n--- tenebra-core service start ---")
	return f, nil
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
