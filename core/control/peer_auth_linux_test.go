//go:build linux

package control

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// writeSeat drops a logind-shaped seat state file (a flat list of KEY=VALUE
// lines) into dir, so the lookup can be driven against fixtures instead of the
// host's live login state.
func writeSeat(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// makeRuntimeDir creates parent/<name> owned by uid, standing in for the
// /run/user/<uid> a session manager creates. Handing a directory to a foreign
// uid needs privilege: the containerised Linux test run is root and exercises
// these cases for real, while an unprivileged run on a developer's desktop can
// only produce directories it owns, so those cases skip rather than assert
// something weaker than they claim.
func makeRuntimeDir(t *testing.T, parent, name string, uid int) {
	t.Helper()
	dir := filepath.Join(parent, name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if os.Getuid() == uid {
		return
	}
	if os.Getuid() != 0 {
		t.Skipf("need privilege to hand a runtime dir to uid %d (running as %d)", uid, os.Getuid())
	}
	if err := os.Chown(dir, uid, uid); err != nil {
		t.Fatalf("chown fixture runtime dir to %d: %v", uid, err)
	}
}

// seat0Body is the shape logind writes for the built-in seat with a session
// active on it, header line included: ACTIVE_UID names the user at the display,
// surrounded by fields — and a comment — the lookup must ignore.
const seat0Body = `# This is private data. Do not parse.
IS_SEAT0=1
CAN_MULTI_SESSION=1
CAN_TTY=1
CAN_GRAPHICAL=1
ACTIVE=2
ACTIVE_UID=1000
SESSIONS=2 1
UIDS=1000 1000
`

// TestConsoleUserUIDFromSeat0: the ordinary desktop case — one built-in seat
// with a logged-in user — resolves to that user's uid, without consulting the
// weaker runtime-directory fallback (pointed at a path that does not exist).
func TestConsoleUserUIDFromSeat0(t *testing.T) {
	seats := filepath.Join(t.TempDir(), "seats")
	writeSeat(t, seats, "seat0", seat0Body)

	got, err := consoleUserUIDIn(seats, filepath.Join(t.TempDir(), "absent"))
	if err != nil {
		t.Fatalf("consoleUserUIDIn: %v", err)
	}
	if got != "1000" {
		t.Errorf("console uid = %q, want %q", got, "1000")
	}
}

// TestConsoleUserUIDSeatWithoutActiveSession: a seat file for a display nobody
// is logged into carries no ACTIVE_UID. That must read as "unknown" — an error
// the policy turns into a fail-open — never as an identity, which an empty peer
// uid could then match.
func TestConsoleUserUIDSeatWithoutActiveSession(t *testing.T) {
	seats := filepath.Join(t.TempDir(), "seats")
	writeSeat(t, seats, "seat0", "IS_SEAT0=1\nCAN_GRAPHICAL=1\n")

	if got, err := consoleUserUIDIn(seats, filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Errorf("console uid = %q, want an error for a seat with no active session", got)
	}
}

// TestConsoleUserUIDNonSeat0: a host whose seat is not named seat0 (a multi-seat
// or unusually configured machine) is still resolved, by the directory scan
// behind the seat0 fast path.
func TestConsoleUserUIDNonSeat0(t *testing.T) {
	seats := filepath.Join(t.TempDir(), "seats")
	writeSeat(t, seats, "seat-virtual", "CAN_GRAPHICAL=1\nACTIVE=7\nACTIVE_UID=1234\n")

	got, err := consoleUserUIDIn(seats, filepath.Join(t.TempDir(), "absent"))
	if err != nil {
		t.Fatalf("consoleUserUIDIn: %v", err)
	}
	if got != "1234" {
		t.Errorf("console uid = %q, want %q", got, "1234")
	}
}

// TestConsoleUserUIDAmbiguousSeatsRefuse: two seats with two different users at
// the machine is exactly the case the lookup must not guess at. It reports the
// ambiguity, and peerAllowed turns that into a logged fail-open rather than
// picking one of the two and locking the other's GUI out.
func TestConsoleUserUIDAmbiguousSeatsRefuse(t *testing.T) {
	seats := filepath.Join(t.TempDir(), "seats")
	writeSeat(t, seats, "seat-a", "ACTIVE_UID=1000\n")
	writeSeat(t, seats, "seat-b", "ACTIVE_UID=1001\n")

	if got, err := consoleUserUIDIn(seats, filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Errorf("console uid = %q, want a refusal when two seats disagree", got)
	}
}

// TestConsoleUserUIDMalformedActiveUID: a value that is not a uid must fail the
// lookup rather than be carried on as an opaque string and compared against a
// peer uid.
func TestConsoleUserUIDMalformedActiveUID(t *testing.T) {
	seats := filepath.Join(t.TempDir(), "seats")
	writeSeat(t, seats, "seat0", "ACTIVE_UID=someone\n")

	if got, err := consoleUserUIDIn(seats, filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Errorf("console uid = %q, want a refusal for a malformed ACTIVE_UID", got)
	}
}

// TestConsoleUserUIDRuntimeDirFallback: with no seat state published (a host
// without logind's seat files) the sole per-user runtime directory answers.
func TestConsoleUserUIDRuntimeDirFallback(t *testing.T) {
	runtimeDir := filepath.Join(t.TempDir(), "user")
	makeRuntimeDir(t, runtimeDir, "1000", 1000)

	got, err := consoleUserUIDIn(filepath.Join(t.TempDir(), "absent-seats"), runtimeDir)
	if err != nil {
		t.Fatalf("consoleUserUIDIn: %v", err)
	}
	if got != "1000" {
		t.Errorf("console uid = %q, want %q (owner of the sole runtime dir)", got, "1000")
	}
}

// TestConsoleUserUIDRuntimeDirTrustsOwnerNotName: the answer comes from the
// directory's owner, not from its name, so a directory labelled with someone
// else's uid cannot nominate that uid as the console user.
func TestConsoleUserUIDRuntimeDirTrustsOwnerNotName(t *testing.T) {
	runtimeDir := filepath.Join(t.TempDir(), "user")
	makeRuntimeDir(t, runtimeDir, "4242", 1000)

	got, err := consoleUserUIDIn(filepath.Join(t.TempDir(), "absent-seats"), runtimeDir)
	if err != nil {
		t.Fatalf("consoleUserUIDIn: %v", err)
	}
	if got != "1000" {
		t.Errorf("console uid = %q, want %q — the owner, not the directory name", got, "1000")
	}
}

// TestConsoleUserUIDSkipsRootRuntimeDir: an administered machine where root also
// holds a session (/run/user/0 from an ssh login) must still resolve the desktop
// user. Root is admitted by peerAllowed's self shortcut anyway, so counting it
// here would only make the lookup ambiguous and fail open on every such host.
func TestConsoleUserUIDSkipsRootRuntimeDir(t *testing.T) {
	runtimeDir := filepath.Join(t.TempDir(), "user")
	makeRuntimeDir(t, runtimeDir, "0", 0)
	makeRuntimeDir(t, runtimeDir, "1001", 1001)

	got, err := consoleUserUIDIn(filepath.Join(t.TempDir(), "absent-seats"), runtimeDir)
	if err != nil {
		t.Fatalf("consoleUserUIDIn: %v", err)
	}
	if got != "1001" {
		t.Errorf("console uid = %q, want %q (root's runtime dir must not count)", got, "1001")
	}
}

// TestConsoleUserUIDAmbiguousRuntimeDirsRefuse: two users with live sessions and
// no seat state is not an identification — the fallback declines instead of
// picking the first one it read.
func TestConsoleUserUIDAmbiguousRuntimeDirsRefuse(t *testing.T) {
	runtimeDir := filepath.Join(t.TempDir(), "user")
	makeRuntimeDir(t, runtimeDir, "1000", 1000)
	makeRuntimeDir(t, runtimeDir, "1001", 1001)

	if got, err := consoleUserUIDIn(filepath.Join(t.TempDir(), "absent-seats"), runtimeDir); err == nil {
		t.Errorf("console uid = %q, want a refusal when two users hold runtime dirs", got)
	}
}

// TestConsoleUserUIDNoSourcesFailsOpen: a machine publishing neither source (no
// logind at all) yields an error, which peerAllowed turns into the documented
// fail-open — the historical any-local-user trust, logged rather than silent.
func TestConsoleUserUIDNoSourcesFailsOpen(t *testing.T) {
	base := t.TempDir()
	lookup := func() (string, error) {
		return consoleUserUIDIn(filepath.Join(base, "no-seats"), filepath.Join(base, "no-run-user"))
	}
	if _, err := lookup(); err == nil {
		t.Fatal("consoleUserUIDIn with neither source present returned no error")
	}

	warned := false
	if !peerAllowed("1000", "0", lookup, func(string) { warned = true }) {
		t.Error("an undeterminable console user must fail open, not lock the GUI out")
	}
	if !warned {
		t.Error("the fail-open must be warned about")
	}
}

// TestConsoleUserUIDSeatWinsOverRuntimeDir: when both sources are present the
// seat state decides. It answers "who is at the display", which is the question;
// a runtime directory only proves a session exists somewhere.
func TestConsoleUserUIDSeatWinsOverRuntimeDir(t *testing.T) {
	seats := filepath.Join(t.TempDir(), "seats")
	writeSeat(t, seats, "seat0", seat0Body)
	runtimeDir := filepath.Join(t.TempDir(), "user")
	makeRuntimeDir(t, runtimeDir, "1001", 1001)

	got, err := consoleUserUIDIn(seats, runtimeDir)
	if err != nil {
		t.Fatalf("consoleUserUIDIn: %v", err)
	}
	if got != "1000" {
		t.Errorf("console uid = %q, want %q from the seat state", got, "1000")
	}
}

// TestDefaultSocketPathIsRunTenebra pins the Linux control-socket path. It is a
// contract with the desktop shell, which dials the same literal string, so a
// change here silently breaks every GUI attach and must be a deliberate edit on
// both sides.
func TestDefaultSocketPathIsRunTenebra(t *testing.T) {
	if DefaultSocketPath != "/run/tenebra.sock" {
		t.Errorf("DefaultSocketPath = %q, want /run/tenebra.sock (the path the desktop shell dials)", DefaultSocketPath)
	}
}

// TestPeerAllowedRejectsOtherLocalUserOnLinux ties the Linux lookup to the
// policy: with a determinable console user, another local account is turned
// away. strconv is used the way authorizePeer does, so the comparison under
// test is the production one — decimal uid strings on both sides.
func TestPeerAllowedRejectsOtherLocalUserOnLinux(t *testing.T) {
	seats := filepath.Join(t.TempDir(), "seats")
	writeSeat(t, seats, "seat0", seat0Body)
	lookup := func() (string, error) {
		return consoleUserUIDIn(seats, filepath.Join(t.TempDir(), "absent"))
	}

	if peerAllowed(strconv.Itoa(1001), "0", lookup, func(string) {
		t.Error("a decisive deny must not warn (that channel means fail-open)")
	}) {
		t.Error("a local user who is neither root nor the seat's active user must be denied")
	}
	if !peerAllowed(strconv.Itoa(1000), "0", lookup, func(msg string) { t.Errorf("unexpected fail-open: %s", msg) }) {
		t.Error("the seat's active user must be admitted")
	}
}
