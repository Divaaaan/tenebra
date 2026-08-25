//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"

	"github.com/Divaaaan/tenebra/core/control"
	"github.com/Divaaaan/tenebra/core/logrot"
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
		// Hand the diagnostics bundle a way to read its own tail back, so a
		// support report from a machine where no UI was ever attached still
		// carries the log the service actually wrote.
		fileLogTail = f.Tail
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

	if err := configureServicePaths(); err != nil {
		log.Printf("fatal: %v", err)
		return false, 1
	}
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
	// The same background work a console-started core does. Missing here, it was
	// missing on every ordinary Windows install: see startBackgroundJobs.
	startBackgroundJobs(ctx, daemon)

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
//
// The file rotates by size (see core/logrot). A service runs for weeks and logs
// every connect decision, so an unbounded append is not a diagnostic but a slow
// disk leak — the failure mode a neighbouring bypass client demonstrated by
// writing 2.66 GB of debug output in forty minutes and then getting in its own
// way. The whole set is capped at logrot's defaults, which is small enough to
// attach to a bug report.
//
// This is the first thing in the process to touch %ProgramData%\Tenebra, and on
// the default-inherited %ProgramData% ACL any standard user can pre-create that
// directory — or the service.log inside it as a junction/symlink — and redirect
// this privileged, LocalSystem-owned write elsewhere. So we clamp the parent to
// the SYSTEM+Administrators-only descriptor (secureDataDir, fail-closed) before
// opening anything inside it, and refuse a pre-existing service.log that a
// trusted principal does not own. The log itself is non-fatal, so the caller
// treats any error here as "run without a log" rather than aborting the service.
func openServiceLog() (*logrot.Writer, error) {
	base := os.Getenv("ProgramData")
	if base == "" {
		base = os.TempDir()
	}
	dir := filepath.Join(base, "Tenebra")
	// Stamp and verify the parent's DACL before opening a path inside it: this
	// evicts a squatter who pre-created the directory and fails closed if the
	// clamp cannot be trusted, so the open below happens in a directory only
	// SYSTEM and Administrators can write.
	if err := secureDataDir(dir); err != nil {
		return nil, fmt.Errorf("secure log directory %s: %w", dir, err)
	}
	logPath := filepath.Join(dir, "service.log")
	// ensureSafeLogTarget runs as logrot's Verify hook rather than once here, so
	// every file the rotation creates is checked the same way the first one is.
	// A rotation is precisely when the path is briefly free for someone else to
	// claim, so checking only at start-up would leave the guarantee with a hole
	// it did not have before rotation existed.
	f, err := logrot.Open(logPath, logrot.Options{Verify: ensureSafeLogTarget})
	if err != nil {
		return nil, err
	}
	fmt.Fprintln(f, "\n--- tenebra-core service start ---")
	return f, nil
}

// ensureSafeLogTarget guards the service.log path against a file an unprivileged
// user planted before the service first clamped the directory — most dangerously
// a junction or symlink redirecting the privileged append into a file the
// attacker chose. Clamping the directory only stops new writes; a reparse point
// or foreign-owned file already sitting at the path survives it. So before the
// open, a reparse point is rejected outright (never trusted, wherever it points)
// and a plain file is trusted only when SYSTEM or Administrators owns it. An
// untrusted entry is rotated aside under a unique name — preserving it for
// forensics while a fresh create wins in the now-restricted directory — and the
// log is refused rather than appended through if it cannot be moved out of the
// way. A file already owned by a trusted principal is left in place so the
// append continues its tail.
func ensureSafeLogTarget(logPath string) error {
	info, err := os.Lstat(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // nothing there; the O_CREATE below owns a fresh file
		}
		return fmt.Errorf("stat %s: %w", logPath, err)
	}
	// Lstat does not follow the link, so this inspects the entry at the path
	// itself. A name-surrogate reparse point (symlink) reports ModeSymlink;
	// other reparse tags (a mount-point junction) report ModeIrregular.
	reparse := info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0
	if !reparse {
		sd, err := windows.GetNamedSecurityInfo(logPath, windows.SE_FILE_OBJECT,
			windows.OWNER_SECURITY_INFORMATION)
		if err != nil {
			return fmt.Errorf("read owner of %s: %w", logPath, err)
		}
		owner, _, err := sd.Owner()
		if err != nil {
			return fmt.Errorf("read owner of %s: %w", logPath, err)
		}
		if owner != nil && ownerIsSystemOrAdmins(owner) {
			return nil
		}
	}
	aside := fmt.Sprintf("%s.suspect-%d", logPath, time.Now().UnixNano())
	if err := os.Rename(logPath, aside); err != nil {
		return fmt.Errorf("rotate untrusted %s aside: %w", logPath, err)
	}
	log.Printf("tenebra-core: rotated untrusted %s to %s", logPath, aside)
	return nil
}

// configureServicePaths pins the machine-scoped locations before buildDaemon
// reads them. A service runs as LocalSystem, so the per-user defaults would
// silently land in the SYSTEM profile where no interactive user (or installer)
// ever looks; instead the store lives under %ProgramData%\Tenebra\data and the
// bundled sing-box is resolved from the install layout around
// tenebra-core.exe. Both knobs are the environment variables every mode
// already honours, set process-wide here so the existing readers — configDir,
// the runner, ruleSetDir — pick them up unchanged; a machine environment set
// by an operator still wins, matching the override semantics everywhere else.
// Console and sidecar runs never come through here and keep their per-user
// paths.
func configureServicePaths() error {
	if os.Getenv("TENEBRA_CONFIG_DIR") == "" {
		base := os.Getenv("ProgramData")
		if base == "" {
			// Unlike the service log this is the credential store: refusing
			// to start beats scattering profiles into a temp fallback.
			return errors.New("service: ProgramData not set; cannot place the data directory")
		}
		dir := filepath.Join(base, "Tenebra", "data")
		if err := secureDataDir(dir); err != nil {
			return fmt.Errorf("service: prepare data dir %s: %w", dir, err)
		}
		os.Setenv("TENEBRA_CONFIG_DIR", dir)
	}
	if os.Getenv("TENEBRA_SINGBOX") == "" {
		exe, err := os.Executable()
		if err != nil {
			return fmt.Errorf("service: locate executable: %w", err)
		}
		if bin := findBundledSingbox(filepath.Dir(exe)); bin != "" {
			// Setting the variable rather than the runner field also lights up
			// ruleSetDir, which probes the directory this path points into.
			os.Setenv("TENEBRA_SINGBOX", bin)
		} else {
			// Not fatal: the runner falls back to sing-box.exe next to the
			// executable and reports precisely, at connect time, if it is
			// missing.
			log.Printf("tenebra-core: no bundled sing-box near %s", exe)
		}
	}
	return nil
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

// dataDirSecurityDescriptor is the DACL stamped on the service's data
// directory: full control for LocalSystem and Administrators, nothing for
// anyone else, and no inherited grants (P). Profiles hold subscription
// credentials, and unprivileged users reach that data through the pipe
// protocol, never the files. OICI carries the grants down to everything the
// store writes inside.
const dataDirSecurityDescriptor = "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)"

// expectedDataDirACEs are the allow ACEs the clamped DACL must carry and nothing
// besides — SYSTEM and Administrators, full control, inherited by children
// (OICI). Verified as SDDL substrings because that is the form the descriptor
// round-trips through, the same shape dataDirSecurityDescriptor was parsed from.
var expectedDataDirACEs = []string{"(A;OICI;FA;;;SY)", "(A;OICI;FA;;;BA)"}

// errUntrustedDataDirOwner marks the one verifyClampedDir failure a privileged
// service clears but an unelevated developer run cannot: the directory's owner
// could not be moved to SYSTEM or Administrators. Tests that drive secureDataDir
// without elevation match on it (errors.Is) to tell an expected unprivileged
// outcome apart from a real regression; production never tolerates it.
var errUntrustedDataDirOwner = errors.New("directory is not SYSTEM- or Administrators-owned")

// secureDataDir creates dir (with parents) and clamps its security descriptor
// to dataDirSecurityDescriptor, then reads the descriptor back and refuses to
// proceed unless the clamp actually took. It secures both the machine data
// store and, reused for its parent, the service log directory. The clamp is
// re-applied on every start, not just on creation: %ProgramData% lets any user
// pre-create the path, and re-stamping the DACL evicts such a squatter before
// the store writes anything. Ownership moves to LocalSystem for the same reason
// — an owner keeps the implicit right to rewrite the DACL — via a best-effort
// assignment (it needs privileges the service has in production and an
// unelevated developer run does not) that the read-back verification then turns
// into a fail-closed guarantee: an attacker-owned directory that survived the
// DACL stamp is rejected here rather than trusted, mirroring the darwin
// verifyRootOnlyDir check.
func secureDataDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	sd, err := windows.SecurityDescriptorFromString(dataDirSecurityDescriptor)
	if err != nil {
		return fmt.Errorf("parse security descriptor: %w", err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return fmt.Errorf("read DACL: %w", err)
	}
	if system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid); err == nil {
		if err := windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
			windows.OWNER_SECURITY_INFORMATION, system, nil, nil, nil); err != nil {
			log.Printf("tenebra-core: keeping the existing owner of %s (%v)", dir, err)
		}
	}
	if err := windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, dacl, nil); err != nil {
		return fmt.Errorf("apply DACL: %w", err)
	}
	got, err := windows.GetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("read back security of %s: %w", dir, err)
	}
	if err := verifyClampedDir(got); err != nil {
		return fmt.Errorf("%s not secured after clamp: %w", dir, err)
	}
	return nil
}

// verifyClampedDir is the fail-closed check secureDataDir ends on, the Windows
// analog of verifyRootOnlyDir: the descriptor just read back from the directory
// must be owned by a trusted principal (SYSTEM or Administrators), carry a
// protected DACL (SE_DACL_PROTECTED — no inherited grants an attacker could have
// seeded up the tree), and grant exactly the two expected full-control ACEs and
// nothing more. A directory an unprivileged squatter pre-created keeps its own
// owner even after the DACL is stamped, and an owner retains the implicit right
// to rewrite that DACL, so an untrusted owner is rejected here rather than left
// for the store to trust. It is pure over the descriptor so the rejection paths
// can be unit-tested without a privileged directory.
func verifyClampedDir(sd *windows.SECURITY_DESCRIPTOR) error {
	control, _, err := sd.Control()
	if err != nil {
		return fmt.Errorf("read control bits: %w", err)
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		return errors.New("DACL is not protected against inheritance")
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return fmt.Errorf("read owner: %w", err)
	}
	if owner == nil || !ownerIsSystemOrAdmins(owner) {
		return fmt.Errorf("%w (owner %s)", errUntrustedDataDirOwner, ownerName(owner))
	}
	s := sd.String()
	for _, ace := range expectedDataDirACEs {
		if !strings.Contains(s, ace) {
			return fmt.Errorf("descriptor %q missing ACE %s", s, ace)
		}
	}
	if got := strings.Count(s, "(A;"); got != len(expectedDataDirACEs) {
		return fmt.Errorf("descriptor %q grants %d allow ACEs, want %d", s, got, len(expectedDataDirACEs))
	}
	return nil
}

// ownerIsSystemOrAdmins reports whether sid is LocalSystem or the builtin
// Administrators group — the two principals the service and its installer run
// as, and the only owners a data or log directory is allowed to have.
func ownerIsSystemOrAdmins(sid *windows.SID) bool {
	return sid.IsWellKnown(windows.WinLocalSystemSid) ||
		sid.IsWellKnown(windows.WinBuiltinAdministratorsSid)
}

// ownerName renders a SID for an error message, tolerating the nil the
// descriptor hands back when it carries no owner at all.
func ownerName(sid *windows.SID) string {
	if sid == nil {
		return "(none)"
	}
	return sid.String()
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
