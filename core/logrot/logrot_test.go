package logrot

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// line makes a payload of exactly n bytes including its newline, so a test can
// reason in whole writes about when the size ceiling is crossed.
func line(tag byte, n int) []byte {
	if n < 1 {
		n = 1
	}
	b := make([]byte, n)
	for i := range b {
		b[i] = tag
	}
	b[n-1] = '\n'
	return b
}

func mustWrite(t *testing.T, w *Writer, p []byte) {
	t.Helper()
	n, err := w.Write(p)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if n != len(p) {
		t.Fatalf("short write: %d of %d", n, len(p))
	}
}

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.Size()
}

// TestRotatesAtTheSizeCeiling is the ceiling itself: past MaxSize the active
// file starts over and the previous contents survive as generation 1.
func TestRotatesAtTheSizeCeiling(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "service.log")
	w, err := Open(path, Options{MaxSize: 100, MaxFiles: 2, MaxTotal: 1 << 20})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer w.Close()

	mustWrite(t, w, line('a', 60))
	if _, err := os.Stat(path + ".1"); !os.IsNotExist(err) {
		t.Fatalf("rotated before the ceiling was reached")
	}
	// 60 + 60 > 100: this write rotates first, so it lands in a fresh file.
	mustWrite(t, w, line('b', 60))

	if got := fileSize(t, path); got != 60 {
		t.Errorf("active file after rotation = %d bytes, want 60", got)
	}
	rotated, err := os.ReadFile(path + ".1")
	if err != nil {
		t.Fatalf("read rotated: %v", err)
	}
	if !strings.HasPrefix(string(rotated), "aaa") {
		t.Errorf("generation 1 does not hold the pre-rotation content: %.10q", rotated)
	}
	active, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read active: %v", err)
	}
	if !strings.HasPrefix(string(active), "bbb") {
		t.Errorf("active file does not hold the post-rotation write: %.10q", active)
	}
}

// TestOldGenerationsAreDeleted covers the file-count budget: rotating more times
// than MaxFiles must not leave a growing pile of numbered logs.
func TestOldGenerationsAreDeleted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "service.log")
	w, err := Open(path, Options{MaxSize: 50, MaxFiles: 2, MaxTotal: 1 << 20})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer w.Close()

	for i := 0; i < 8; i++ {
		mustWrite(t, w, line(byte('0'+i), 40))
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) != 3 { // active + 2 generations
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Fatalf("kept %d files (%v), want 3", len(entries), names)
	}
	if _, err := os.Stat(path + ".3"); !os.IsNotExist(err) {
		t.Errorf("generation 3 survived a MaxFiles=2 configuration")
	}
	// The two survivors must be the newest ones, not the oldest: the write
	// immediately before the last rotation is what usually explains a crash.
	g1, err := os.ReadFile(path + ".1")
	if err != nil {
		t.Fatalf("read generation 1: %v", err)
	}
	if !strings.HasPrefix(string(g1), "6") {
		t.Errorf("generation 1 = %.4q, want the second-newest write", g1)
	}
}

// TestTotalBudgetDropsTheOldest covers the byte budget independently of the file
// count: a MaxTotal smaller than MaxFiles×MaxSize still binds.
func TestTotalBudgetDropsTheOldest(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "service.log")
	w, err := Open(path, Options{MaxSize: 100, MaxFiles: 5, MaxTotal: 250})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer w.Close()

	for i := 0; i < 6; i++ {
		mustWrite(t, w, line(byte('0'+i), 90))
	}

	var total int64
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, e := range entries {
		total += fileSize(t, filepath.Join(dir, e.Name()))
	}
	if total > 250 {
		t.Errorf("kept %d bytes across %d files, want <= 250", total, len(entries))
	}
	if len(entries) < 2 {
		t.Errorf("budget pruned down to %d files; the previous session should survive", len(entries))
	}
}

// TestActiveFileLosesNoWrites is the promise that matters most: rotation happens
// underneath the writer, and every line handed to it is still readable
// afterwards, in order, across the whole set.
func TestActiveFileLosesNoWrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "service.log")
	w, err := Open(path, Options{MaxSize: 200, MaxFiles: 20, MaxTotal: 1 << 20})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer w.Close()

	const writes = 200
	for i := 0; i < writes; i++ {
		if _, err := fmt.Fprintf(w, "line %04d\n", i); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}

	var all []string
	// Newest generation last, so reading generations high-to-low then the active
	// file reconstructs the original order.
	for i := 20; i >= 1; i-- {
		b, err := os.ReadFile(generation(path, i))
		if err != nil {
			continue
		}
		all = append(all, strings.Split(strings.TrimRight(string(b), "\n"), "\n")...)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read active: %v", err)
	}
	all = append(all, strings.Split(strings.TrimRight(string(b), "\n"), "\n")...)

	if len(all) != writes {
		t.Fatalf("recovered %d lines, wrote %d", len(all), writes)
	}
	for i, got := range all {
		want := fmt.Sprintf("line %04d", i)
		if got != want {
			t.Fatalf("line %d = %q, want %q", i, got, want)
		}
	}
}

// TestConcurrentWritesSurviveRotation drives Write from several goroutines while
// the file rotates repeatedly. Every line must land whole somewhere: a rotation
// that ran while another goroutine held a stale descriptor would show up as a
// missing or spliced line.
func TestConcurrentWritesSurviveRotation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "service.log")
	w, err := Open(path, Options{MaxSize: 512, MaxFiles: 50, MaxTotal: 1 << 20})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer w.Close()

	const (
		writers = 8
		each    = 100
	)
	var wg sync.WaitGroup
	for g := 0; g < writers; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				if _, err := fmt.Fprintf(w, "w%02d-i%03d\n", g, i); err != nil {
					t.Errorf("write: %v", err)
					return
				}
			}
		}(g)
	}
	wg.Wait()

	seen := map[string]bool{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		for _, ln := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
			if ln == "" {
				continue
			}
			if seen[ln] {
				t.Fatalf("line %q appeared twice", ln)
			}
			seen[ln] = true
		}
	}
	for g := 0; g < writers; g++ {
		for i := 0; i < each; i++ {
			want := fmt.Sprintf("w%02d-i%03d", g, i)
			if !seen[want] {
				t.Fatalf("line %q was lost across rotation", want)
			}
		}
	}
}

// TestVerifyRunsOnEveryOpen pins the security property the Windows service
// depends on: the hook that refuses a planted service.log is re-checked after a
// rotation, not only at start-up. Rotation creates a brand-new file, which is
// exactly the moment a squatter could win the path.
func TestVerifyRunsOnEveryOpen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "service.log")
	var calls int
	w, err := Open(path, Options{
		MaxSize:  60,
		MaxFiles: 2,
		Verify:   func(string) error { calls++; return nil },
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer w.Close()
	if calls != 1 {
		t.Fatalf("verify ran %d times at open, want 1", calls)
	}
	mustWrite(t, w, line('a', 50))
	mustWrite(t, w, line('b', 50)) // rotates
	if calls != 2 {
		t.Errorf("verify ran %d times, want 2 (open + rotation)", calls)
	}
}

// TestVerifyRefusalIsReported: a hook that rejects the path must fail Open
// rather than leave a writer appending through whatever is there.
func TestVerifyRefusalIsReported(t *testing.T) {
	dir := t.TempDir()
	sentinel := errors.New("untrusted owner")
	_, err := Open(filepath.Join(dir, "service.log"), Options{
		Verify: func(string) error { return sentinel },
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("open error = %v, want the verify refusal", err)
	}
}

// TestTailReadsAcrossGenerations: the diagnostics bundle asks for the last N
// lines right after a rotation too, when most of them live in generation 1.
func TestTailReadsAcrossGenerations(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "service.log")
	w, err := Open(path, Options{MaxSize: 120, MaxFiles: 3, MaxTotal: 1 << 20})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer w.Close()

	for i := 0; i < 40; i++ {
		if _, err := fmt.Fprintf(w, "line %02d\n", i); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	got := w.Tail(10)
	if len(got) != 10 {
		t.Fatalf("tail returned %d lines, want 10: %v", len(got), got)
	}
	for i, ln := range got {
		want := fmt.Sprintf("line %02d", 30+i)
		if ln != want {
			t.Fatalf("tail[%d] = %q, want %q (full: %v)", i, ln, want, got)
		}
	}
}

// TestTailOnAFreshFileIsEmpty: nothing written yet must not look like one blank
// line in a support bundle.
func TestTailOnAFreshFileIsEmpty(t *testing.T) {
	w, err := Open(filepath.Join(t.TempDir(), "service.log"), Options{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer w.Close()
	if got := w.Tail(20); len(got) != 0 {
		t.Errorf("tail of an empty log = %v, want none", got)
	}
}

// TestOpenPrunesLeftoverGenerations: a build that tightened its limits must
// dispose of the wider set the previous build left behind, without waiting for
// a rotation that may be hours away.
func TestOpenPrunesLeftoverGenerations(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "service.log")
	for i := 1; i <= 6; i++ {
		if err := os.WriteFile(generation(path, i), line('x', 100), 0o644); err != nil {
			t.Fatalf("seed generation %d: %v", i, err)
		}
	}
	w, err := Open(path, Options{MaxSize: 1000, MaxFiles: 2, MaxTotal: 10_000})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer w.Close()

	for i := 3; i <= 6; i++ {
		if _, err := os.Stat(generation(path, i)); !os.IsNotExist(err) {
			t.Errorf("generation %d survived a MaxFiles=2 configuration", i)
		}
	}
	for i := 1; i <= 2; i++ {
		if _, err := os.Stat(generation(path, i)); err != nil {
			t.Errorf("generation %d was pruned but is within MaxFiles=2", i)
		}
	}
}

// TestAppendKeepsTheExistingTail: reopening must not truncate. A crash loop that
// wiped the log on every restart would destroy the only record of why it is
// looping.
func TestAppendKeepsTheExistingTail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "service.log")
	if err := os.WriteFile(path, []byte("earlier session\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	w, err := Open(path, Options{MaxSize: 1 << 20})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	mustWrite(t, w, []byte("this session\n"))
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got := string(b); got != "earlier session\nthis session\n" {
		t.Errorf("file = %q, want both sessions", got)
	}
}

// TestWriteAfterCloseIsRefused: a closed writer must say so rather than silently
// swallow the lines handed to it.
func TestWriteAfterCloseIsRefused(t *testing.T) {
	w, err := Open(filepath.Join(t.TempDir(), "service.log"), Options{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Errorf("second close = %v, want nil (idempotent)", err)
	}
	if _, err := w.Write([]byte("x\n")); !errors.Is(err, os.ErrClosed) {
		t.Errorf("write after close = %v, want os.ErrClosed", err)
	}
}

// TestMaxFilesNegativeKeepsNoGenerations: a caller that wants a hard ceiling and
// no history gets exactly one file.
func TestMaxFilesNegativeKeepsNoGenerations(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "service.log")
	w, err := Open(path, Options{MaxSize: 60, MaxFiles: -1})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer w.Close()

	for i := 0; i < 5; i++ {
		mustWrite(t, w, line('a', 50))
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("kept %d files, want 1", len(entries))
	}
	if got := fileSize(t, path); got > 60 {
		t.Errorf("active file = %d bytes, want <= 60", got)
	}
}
