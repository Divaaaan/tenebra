//go:build windows

package zapret

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// winwsImage is the process image every strategy in the bundle launches.
const winwsImage = "winws.exe"

// winwsProcess is one running winws.exe: the process id and the executable it
// runs. Path is empty when the path could not be read — see imagePath, which
// explains why that is not the same as "no path".
type winwsProcess struct {
	PID  uint32
	Path string
}

// listWinws enumerates the running winws.exe processes together with the image
// each one was started from.
//
// The name alone is not enough for anything this package does with the answer.
// zapret is distributed as a folder anyone can unpack anywhere, so a machine can
// easily be running a copy the user started by hand next to the one installed
// here; both are winws.exe. Deciding by name means killing a stranger's bypass
// and reading it as our own, which is exactly what the tasklist/taskkill pair
// this replaced did.
//
// The process snapshot is used rather than a console tool because it is the only
// form that yields a pid to open — a pid is what terminating one process instead
// of every process with this name needs — and because the tool that could report
// an image path, wmic, has been removed from current Windows.
func listWinws() ([]winwsProcess, error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = windows.CloseHandle(snap) }()

	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	var out []winwsProcess
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		if !strings.EqualFold(windows.UTF16ToString(e.ExeFile[:]), winwsImage) {
			continue
		}
		out = append(out, winwsProcess{PID: e.ProcessID, Path: imagePath(e.ProcessID)})
	}
	// The walk ends by running out of entries; anything else means the snapshot
	// is not a list we may act on.
	if err != nil && !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return nil, err
	}
	return out, nil
}

// imagePath reads the full path of the executable a process is running, or ""
// when it cannot be read.
//
// A process whose path is unknown is never treated as ours. That asymmetry is
// deliberate. The only thing this package does with the answer is decide what to
// terminate, and the two mistakes are not comparable: refusing to kill one of
// ours costs a strategy that fails to start and is reported as not started,
// while killing someone else's takes down a bypass this program did not launch
// and has no way to bring back. An unreadable path is also what a foreign winws
// most often looks like from here — the bundle can install itself as a service,
// and a LocalSystem process is not one an unelevated core may open — so guessing
// "probably ours" would land on precisely the processes that are not.
//
// The opposite direction stays intact because our own winws is a descendant of
// this process: whatever token the core holds, the winws its batch started holds
// the same one, so its path reads back.
func imagePath(pid uint32) string {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return ""
	}
	defer func() { _ = windows.CloseHandle(h) }()

	buf := make([]uint16, windows.MAX_PATH)
	for {
		size := uint32(len(buf))
		err := windows.QueryFullProcessImageName(h, 0, &buf[0], &size)
		if err == nil {
			return windows.UTF16ToString(buf[:size])
		}
		if !errors.Is(err, windows.ERROR_INSUFFICIENT_BUFFER) || len(buf) >= windows.MAX_LONG_PATH {
			return ""
		}
		buf = make([]uint16, len(buf)*2)
	}
}

// comparablePath renders a Windows path in the form two spellings of the same
// location share.
//
// Three things differ between the directory this runner was configured with and
// the path a running process reports, and all three are cosmetic: case, which
// Windows does not distinguish; the separator, since the API accepts both and
// callers mix them; and 8.3 short names, which is how a path picked up from an
// environment variable or a batch file's %~dp0 can come back as PROGRA~1 for the
// same folder. Comparing the raw strings makes our own process look foreign the
// first time any of the three differs — a bypass that then cannot be stopped.
//
// GetLongPathName only answers for a path that exists; when it does not, the
// cleaned absolute form is the best available and is still correct for every
// comparison where neither side has a short name.
func comparablePath(p string) string {
	if p == "" {
		return ""
	}
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	if long := longPath(p); long != "" {
		p = long
	}
	return strings.ToLower(filepath.Clean(p))
}

// longPath expands the 8.3 components of an existing path, or returns "" when
// Windows cannot resolve it.
func longPath(p string) string {
	u, err := windows.UTF16PtrFromString(p)
	if err != nil {
		return ""
	}
	buf := make([]uint16, windows.MAX_PATH)
	for {
		// On success n is the length written, without the terminator; when the
		// buffer is too small n is the size required, with it.
		n, err := windows.GetLongPathName(u, &buf[0], uint32(len(buf)))
		if err != nil || n == 0 {
			return ""
		}
		if int(n) <= len(buf) {
			return windows.UTF16ToString(buf[:n])
		}
		if int(n) > windows.MAX_LONG_PATH {
			return ""
		}
		buf = make([]uint16, n)
	}
}

// underDir reports whether path names a file somewhere inside dir. The bundle
// keeps winws.exe in bin/, so the test is containment at any depth rather than a
// single directory.
func underDir(dir, path string) bool {
	d, p := comparablePath(dir), comparablePath(path)
	if d == "" || p == "" {
		return false
	}
	if !strings.HasSuffix(d, string(os.PathSeparator)) {
		d += string(os.PathSeparator)
	}
	return strings.HasPrefix(p, d)
}

// ownWinws picks out of an enumeration the processes running the winws.exe that
// belongs to the bundle in dir. It is separate from the enumeration so the rule
// can be tested without processes.
func ownWinws(dir string, procs []winwsProcess) []uint32 {
	var pids []uint32
	for _, p := range procs {
		if underDir(dir, p.Path) {
			pids = append(pids, p.PID)
		}
	}
	return pids
}

// ownWinwsRunning reports whether the bundle in dir currently has a winws
// running. A snapshot that cannot be taken answers no, the same way the tasklist
// it replaced did — an unanswerable question is not evidence that the filter is
// attached.
func ownWinwsRunning(dir string) bool {
	procs, err := listWinws()
	if err != nil {
		return false
	}
	return len(ownWinws(dir, procs)) > 0
}

// killOwnWinws terminates the winws processes started from the bundle in dir and
// leaves every other one alone.
//
// Best effort per process: one that exits between the snapshot and the open, or
// that we may not terminate, is skipped rather than aborting the rest — the
// caller then waits on ownWinwsRunning and finds out whether the bypass is
// actually down.
func killOwnWinws(dir string) {
	procs, err := listWinws()
	if err != nil {
		return
	}
	for _, pid := range ownWinws(dir, procs) {
		h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, pid)
		if err != nil {
			continue
		}
		_ = windows.TerminateProcess(h, 1)
		_ = windows.CloseHandle(h)
	}
}
