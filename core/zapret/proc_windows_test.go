//go:build windows

package zapret

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// standInEnv marks the re-executed test binary as the stand-in process rather
// than an ordinary test run.
const standInEnv = "TENEBRA_WINWS_STAND_IN"

// TestWinwsStandInProcess is not a test. It is the body of the process
// standInWinws starts: a copy of this test binary living at
// <dir>\bin\winws.exe, which is what the ownership rule has to be exercised
// against — a real entry in the process snapshot with a real image path, from a
// directory the test controls. Started without the marker (an ordinary `go
// test` run) it skips and costs nothing.
//
// The stand-in is used instead of the real winws deliberately: winws attaches a
// kernel packet filter and one may already be running on the machine for the
// user's own bypass, so a test that started or killed the genuine article would
// cut the network of whoever ran it.
func TestWinwsStandInProcess(t *testing.T) {
	if os.Getenv(standInEnv) != "1" {
		t.Skip("helper process for TestListWinwsSeesAForeignProcessAndOwnWinwsSortsIt")
	}
	// Outlives any run of the tests that start it; they kill it when done.
	time.Sleep(60 * time.Second)
}

// standInWinws plants a copy of this test binary at dir\bin\winws.exe — the
// exact layout the bundle installs — starts it, and returns its path and pid.
// It is killed when the test ends whatever happens, so a failing assertion
// cannot leave a process named winws.exe behind.
func standInWinws(t *testing.T, dir string) (string, uint32) {
	t.Helper()

	self, err := os.Executable()
	if err != nil {
		t.Fatalf("locate the test binary: %v", err)
	}
	body, err := os.ReadFile(self)
	if err != nil {
		t.Fatalf("read the test binary: %v", err)
	}
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", bin, err)
	}
	path := filepath.Join(bin, winwsImage)
	if err := os.WriteFile(path, body, 0o755); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}

	cmd := exec.Command(path, "-test.run=TestWinwsStandInProcess")
	cmd.Env = append(os.Environ(), standInEnv+"=1")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s: %v", path, err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return path, uint32(cmd.Process.Pid)
}

// TestOwnWinwsMatchesOnlyTheBundleDirectory is the rule the whole fix rests on:
// a winws is ours when its image sits inside this runner's bundle, and the
// spellings Windows hands back for the same location must not change the answer.
func TestOwnWinwsMatchesOnlyTheBundleDirectory(t *testing.T) {
	dir := t.TempDir()
	ours := filepath.Join(dir, "bin", winwsImage)
	elsewhere := t.TempDir()

	cases := []struct {
		name string
		path string
		want bool
	}{
		{"the bundle's own winws", ours, true},
		{"upper case, which Windows does not distinguish", strings.ToUpper(ours), true},
		{"forward slashes", filepath.ToSlash(ours), true},
		{"an unnormalized path through the parent", filepath.Join(dir, "bin", "..", "bin", winwsImage), true},
		{"straight in the bundle root", filepath.Join(dir, winwsImage), true},
		{"another unpacked copy of zapret", filepath.Join(elsewhere, "bin", winwsImage), false},
		{"a path that could not be read", "", false},
		{"a sibling whose name starts with ours", filepath.Join(dir+"-other", "bin", winwsImage), false},
		{"the bundle directory itself", dir, false},
	}
	for _, c := range cases {
		got := ownWinws(dir, []winwsProcess{{PID: 4242, Path: c.path}})
		if (len(got) == 1) != c.want {
			t.Errorf("%s: ownWinws(%q, %q) = %v, want ours=%v", c.name, dir, c.path, got, c.want)
		}
	}
}

// TestOwnWinwsSeesThroughShortNames covers the spelling that is not cosmetic to
// a string comparison: a path picked up from a batch file or an environment
// variable can arrive in 8.3 form (PROGRA~1) for the very same folder. Left
// unresolved, our own process reads as a stranger's and the bypass cannot be
// switched off.
func TestOwnWinwsSeesThroughShortNames(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "zapret-discord-youtube-1.10.2")
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", bin, err)
	}
	long := filepath.Join(bin, winwsImage)
	if err := os.WriteFile(long, []byte("x"), 0o644); err != nil {
		t.Fatalf("write %s: %v", long, err)
	}

	short := shortPathName(t, long)
	if strings.EqualFold(short, long) {
		t.Skip("this volume generates no 8.3 names, so there is no short spelling to resolve")
	}
	if got := ownWinws(dir, []winwsProcess{{PID: 4242, Path: short}}); len(got) != 1 {
		t.Errorf("a process reported as %q was not recognised as belonging to %q", short, dir)
	}
	// And from the other side: the bundle directory itself may be the one named
	// in short form.
	shortDir := shortPathName(t, dir)
	if got := ownWinws(shortDir, []winwsProcess{{PID: 4242, Path: long}}); len(got) != 1 {
		t.Errorf("a process reported as %q was not recognised as belonging to %q", long, shortDir)
	}
}

// shortPathName asks Windows for the 8.3 spelling of an existing path, or
// returns it unchanged when the volume has none.
func shortPathName(t *testing.T, p string) string {
	t.Helper()
	u, err := windows.UTF16PtrFromString(p)
	if err != nil {
		t.Fatalf("encode %q: %v", p, err)
	}
	buf := make([]uint16, windows.MAX_PATH)
	n, err := windows.GetShortPathName(u, &buf[0], uint32(len(buf)))
	if err != nil || n == 0 || int(n) > len(buf) {
		return p
	}
	return windows.UTF16ToString(buf[:n])
}

// TestListWinwsSeesAForeignProcessAndOwnWinwsSortsIt drives the native path end
// to end against a process that really exists: the snapshot has to find a
// winws.exe by name, the image path has to come back, and the directory filter
// has to claim it for the folder it was started from and for no other.
//
// This is the half that a table of strings cannot prove — that the enumeration
// and the path query work at all — and it is what the old tasklist/taskkill pair
// got wrong: every winws.exe on the machine was "ours".
func TestListWinwsSeesAForeignProcessAndOwnWinwsSortsIt(t *testing.T) {
	theirs := t.TempDir()
	ourBundle := t.TempDir()
	path, pid := standInWinws(t, theirs)

	procs := waitForStandIn(t, pid)

	var found *winwsProcess
	for i := range procs {
		if procs[i].PID == pid {
			found = &procs[i]
		}
	}
	if found == nil {
		t.Fatalf("the snapshot listed %d winws processes and none was the stand-in (pid %d)", len(procs), pid)
	}
	if !strings.EqualFold(found.Path, path) {
		t.Fatalf("the stand-in's image read back as %q, want %q", found.Path, path)
	}

	if got := ownWinws(theirs, procs); len(got) != 1 || got[0] != pid {
		t.Errorf("ownWinws(%q) = %v, want exactly the stand-in (pid %d)", theirs, got, pid)
	}
	// The point of the fix: a winws that exists but was started from somewhere
	// else is not this runner's, no matter how many of them are running.
	if got := ownWinws(ourBundle, procs); len(got) != 0 {
		t.Errorf("ownWinws(%q) claimed %v; a bundle directory with nothing running owns no process", ourBundle, got)
	}
	if ownWinwsRunning(ourBundle) {
		t.Errorf("ownWinwsRunning(%q) is true with a foreign winws running and none of its own", ourBundle)
	}
	if !ownWinwsRunning(theirs) {
		t.Errorf("ownWinwsRunning(%q) is false while its own stand-in is running", theirs)
	}
}

// TestKillOwnWinwsSparesEverybodyElses is the consequence that costs a user
// their connection when it is wrong: stopping the bypass must leave a winws
// this runner did not start alone, and must still kill the one it did.
func TestKillOwnWinwsSparesEverybodyElses(t *testing.T) {
	theirs := t.TempDir()
	ourBundle := t.TempDir()
	_, pid := standInWinws(t, theirs)
	waitForStandIn(t, pid)

	// A stop on a bundle directory of our own: the stranger's process is not in
	// it and must survive.
	killOwnWinws(ourBundle)
	// Terminating is asynchronous; give a wrong implementation every chance to
	// show the kill before declaring the process spared.
	time.Sleep(500 * time.Millisecond)
	if !ownWinwsRunning(theirs) {
		t.Fatalf("stopping the bypass in %q killed the winws running from %q", ourBundle, theirs)
	}

	// And the same call aimed at the directory that does own it takes it down,
	// so the sparing above is not simply a kill that never works.
	killOwnWinws(theirs)
	deadline := time.Now().Add(5 * time.Second)
	for ownWinwsRunning(theirs) {
		if time.Now().After(deadline) {
			t.Fatalf("killOwnWinws(%q) left its own process running", theirs)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// waitForStandIn polls the snapshot until the stand-in shows up, and returns the
// enumeration that contains it. A process is visible almost immediately after
// CreateProcess returns, but polling keeps the test off a timing assumption.
func waitForStandIn(t *testing.T, pid uint32) []winwsProcess {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		procs, err := listWinws()
		if err != nil {
			t.Fatalf("listWinws: %v", err)
		}
		for _, p := range procs {
			if p.PID == pid {
				return procs
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the stand-in (pid %d) never appeared in the process snapshot", pid)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestImagePathRefusesToGuess: a process whose path cannot be read — access
// denied, or a pid that died between the snapshot and the open — reports "", and
// "" is never inside any directory. That is the safe direction, and it is the
// direction the machine actually goes: a zapret installed as a service runs as
// LocalSystem, which an unelevated core may not open at all.
func TestImagePathRefusesToGuess(t *testing.T) {
	// Pid 0 is the idle process: it always exists and never opens.
	if got := imagePath(0); got != "" {
		t.Errorf("imagePath(0) = %q, want an empty path for a process that cannot be opened", got)
	}
	if underDir(t.TempDir(), "") {
		t.Error("an unread path was treated as living inside a bundle directory")
	}
	if underDir("", filepath.Join(t.TempDir(), "bin", winwsImage)) {
		t.Error("an empty bundle directory claimed a process; it would claim every process")
	}
}

// TestOwnWinwsSeesThroughShortNamesWhenTheImageIsGone is the case that got past
// the suite above and failed on CI.
//
// The short-name test before this one writes the executable first, so both sides
// of the comparison exist and both expand. Nothing existed to force the other
// arrangement — a directory that resolves next to an image path that does not —
// until a runner supplied it: GitHub's Windows image puts the temp directory
// under C:\Users\RUNNER~1, so every path in the suite carried a short component,
// the bundle directory expanded to the account's real name and the path naming
// winws.exe inside it did not. Same folder, unequal strings, and every case in
// the table failed at once.
//
// That is not a test artefact. A process's image can be replaced or deleted while
// it runs, and then the path Windows reports for it no longer resolves either —
// on a machine with 8.3 enabled, which is the default, that is a live bypass that
// can no longer be switched off.
func TestOwnWinwsSeesThroughShortNamesWhenTheImageIsGone(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "zapret-discord-youtube-1.10.2")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	shortDir := shortPathName(t, dir)
	if strings.EqualFold(shortDir, dir) {
		t.Skip("this volume generates no 8.3 names, so there is no short spelling to resolve")
	}

	// Never written: this is the image of a process whose file is no longer there.
	absent := filepath.Join(shortDir, "bin", winwsImage)

	if got := ownWinws(dir, []winwsProcess{{PID: 4242, Path: absent}}); len(got) != 1 {
		t.Errorf("a process reported as %q was not recognised as belonging to %q\n"+
			"(the directory resolves, the missing image does not, and the two spellings diverge)",
			absent, dir)
	}
	// The mirror image: the runner is configured with the short spelling and the
	// process reports the long one, with the leaf still absent.
	longAbsent := filepath.Join(dir, "bin", winwsImage)
	if got := ownWinws(shortDir, []winwsProcess{{PID: 4242, Path: longAbsent}}); len(got) != 1 {
		t.Errorf("a process reported as %q was not recognised as belonging to %q", longAbsent, shortDir)
	}
}
