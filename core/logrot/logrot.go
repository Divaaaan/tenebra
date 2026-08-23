// Package logrot writes an append-only log file that rotates itself by size.
//
// It exists because a log with no ceiling is not a diagnostic, it is a second
// fault. A DPI-bypass client on the author's machine wrote 2.66 GB of debug
// output in forty minutes and then spent its time fighting its own disk; the
// tunnel it was supposed to be carrying suffered for it. A log that cannot be
// read and cannot stop growing has failed twice over.
//
// So the writer keeps three promises. The active file never grows far past
// MaxSize. The whole set — active plus rotated — never exceeds MaxTotal or
// MaxFiles+1 files. And nothing that was already written is thrown away to make
// room until it is the oldest thing there, because the tail of the previous
// session is usually the part that explains the crash.
//
// Rotation is close-rename-open under a mutex, in that order, because a file
// cannot be renamed on Windows while this process still holds it open, and
// because a concurrent Write must never land in a file that is being moved out
// from under it. Callers hand a *Writer to log.SetOutput and forget about it.
package logrot

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// Defaults sized for a support artefact rather than a data lake: eight
// megabytes is a few hundred thousand log lines, which is far more than anyone
// reads and still small enough to attach to a bug report, and four generations
// covers a couple of days of ordinary use plus the session that broke.
const (
	DefaultMaxSize  = 8 << 20  // 8 MiB per file
	DefaultMaxFiles = 3        // rotated generations kept beside the active file
	DefaultMaxTotal = 32 << 20 // 32 MiB across the whole set
)

// retryGapDivisor sets how far the active file may grow past MaxSize before a
// rotation that failed is attempted again. A rename can fail for reasons this
// process cannot fix — another program holding the target open is the usual one
// — and retrying on every write would turn a stuck rename into a syscall storm.
// Waiting for another MaxSize/4 bounds the overshoot per retry while keeping the
// writer honest about wanting to rotate.
const retryGapDivisor = 4

// tailWindow caps how much of a file Tail reads back from the end. Enough for a
// few thousand lines, small enough that a diagnostics bundle never pulls a
// multi-megabyte file into memory.
const tailWindow = 512 << 10

// Options configures a Writer. A zero value is valid: every field falls back to
// its Default* constant, and no verification hook runs.
type Options struct {
	// MaxSize is the byte ceiling for the active file. A write that would carry
	// the file past it rotates first, so a single oversized line still lands
	// whole rather than being split across generations.
	MaxSize int64
	// MaxFiles is how many rotated generations are kept beside the active file
	// (path.1 … path.MaxFiles). Zero uses DefaultMaxFiles; a negative value keeps
	// none, so the active file is simply restarted when it fills.
	MaxFiles int
	// MaxTotal is the byte budget for the active file plus every rotated one.
	// The oldest generations are deleted until the set fits. It is not redundant
	// with MaxSize×MaxFiles: it also disposes of files left behind by a build
	// configured with larger limits.
	MaxTotal int64
	// Verify, when set, is called with the log path immediately before the file
	// is opened — at Open and again after every rotation — and a non-nil error
	// aborts that open. It is the hook the Windows service uses to refuse a
	// service.log that an unprivileged user planted at the path (a symlink, or a
	// file a foreign principal owns). Rotation creates a fresh file every time it
	// runs, so re-checking on each one is what keeps that guarantee from being a
	// start-up-only formality.
	Verify func(path string) error
}

// withDefaults returns o with unset fields filled in.
func (o Options) withDefaults() Options {
	if o.MaxSize <= 0 {
		o.MaxSize = DefaultMaxSize
	}
	if o.MaxFiles == 0 {
		o.MaxFiles = DefaultMaxFiles
	}
	if o.MaxFiles < 0 {
		o.MaxFiles = 0
	}
	if o.MaxTotal <= 0 {
		o.MaxTotal = DefaultMaxTotal
	}
	return o
}

// Writer is an io.WriteCloser over a size-rotated log file. It is safe for
// concurrent use; the standard library's log.Logger serialises its own writes,
// but the tunnel writes from several goroutines and the mutex is what makes a
// rotation atomic with respect to all of them.
type Writer struct {
	path string
	opt  Options

	mu sync.Mutex
	f  *os.File
	// size is the active file's length in bytes, tracked rather than stat'ed so
	// the common path costs no syscall.
	size int64
	// retryAt is the size at which a rotation that could not rename will be
	// tried again; zero means "rotate as soon as MaxSize is passed".
	retryAt int64
}

// Open opens path for appending, creating it and its parent directory if
// needed, and prunes anything the current limits no longer allow. The returned
// Writer owns the file until Close.
func Open(path string, opt Options) (*Writer, error) {
	opt = opt.withDefaults()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create log directory: %w", err)
	}
	w := &Writer{path: path, opt: opt}
	if err := w.open(); err != nil {
		return nil, err
	}
	// A build may have shipped with tighter limits than the one that wrote the
	// files already on disk, so the budget is enforced at start rather than only
	// after the first rotation.
	w.prune()
	return w, nil
}

// Path returns the active log file's path.
func (w *Writer) Path() string { return w.path }

// open runs the verification hook and opens the active file for appending,
// recording its current length. The caller holds mu (or has not published w
// yet).
func (w *Writer) open() error {
	if w.opt.Verify != nil {
		if err := w.opt.Verify(w.path); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	size := int64(0)
	if info, err := f.Stat(); err == nil {
		size = info.Size()
	}
	w.f, w.size, w.retryAt = f, size, 0
	return nil
}

// Write appends p, rotating first when it would carry the active file past
// MaxSize. A rotation failure is never allowed to lose the write: the writer
// falls back to the file it already has and tries again once the file has grown
// another quarter of MaxSize.
func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.f == nil {
		return 0, os.ErrClosed
	}
	// An empty file is never rotated: doing so on an oversized single write
	// would produce an empty generation and still write the line in full.
	if w.size > 0 && w.size+int64(len(p)) > w.opt.MaxSize && w.size >= w.retryAt {
		if err := w.rotate(); err != nil {
			// Keep logging into the file we still hold rather than dropping the
			// line; back the next attempt off so a permanently unrenameable file
			// does not cost a syscall per write.
			w.retryAt = w.size + w.opt.MaxSize/retryGapDivisor
		}
	}
	n, err := w.f.Write(p)
	w.size += int64(n)
	return n, err
}

// rotate moves the active file to path.1 (shifting the older generations up),
// opens a fresh one, and prunes the set back inside its limits. The caller holds
// mu.
//
// The active file is closed before the rename because Windows refuses to rename
// a file this process still has open — the failure this ordering avoids is the
// one where rotation silently never happens on the only platform that ships a
// service.
func (w *Writer) rotate() error {
	if err := w.f.Close(); err != nil {
		// The handle is gone as far as we are concerned either way; reopening
		// below is what restores a usable writer.
		_ = err
	}
	w.f = nil

	if w.opt.MaxFiles == 0 {
		// No generations kept: the active file is simply restarted.
		if err := os.Remove(w.path); err != nil && !os.IsNotExist(err) {
			return w.reopenAfterFailedRotate(err)
		}
		if err := w.open(); err != nil {
			return err
		}
		return nil
	}

	// Shift the generations up, oldest first, so each rename lands on a name that
	// has just been vacated. The one falling off the end is deleted.
	_ = os.Remove(generation(w.path, w.opt.MaxFiles))
	for i := w.opt.MaxFiles - 1; i >= 1; i-- {
		from, to := generation(w.path, i), generation(w.path, i+1)
		if _, err := os.Stat(from); err != nil {
			continue
		}
		_ = os.Rename(from, to)
	}
	if err := os.Rename(w.path, generation(w.path, 1)); err != nil {
		return w.reopenAfterFailedRotate(err)
	}
	if err := w.open(); err != nil {
		return err
	}
	w.prune()
	return nil
}

// reopenAfterFailedRotate restores a usable writer after a rotation step failed
// and returns cause, so Write can back the next attempt off. If even the reopen
// fails the writer is left closed and every later Write reports it, which is the
// honest outcome — silently discarding log lines is how a diagnostic becomes a
// lie.
func (w *Writer) reopenAfterFailedRotate(cause error) error {
	if err := w.open(); err != nil {
		return fmt.Errorf("rotate %s: %v; reopen: %w", w.path, cause, err)
	}
	return fmt.Errorf("rotate %s: %w", w.path, cause)
}

// prune enforces the file-count and total-size budgets, deleting the oldest
// generations first. Errors are ignored: a file that cannot be removed is a
// disk-level problem, and refusing to log because of it would be a worse
// outcome than being slightly over budget. The caller holds mu.
func (w *Writer) prune() {
	// Anything numbered past MaxFiles is left over from a wider configuration.
	for i := w.opt.MaxFiles + 1; i <= w.opt.MaxFiles+16; i++ {
		p := generation(w.path, i)
		if _, err := os.Stat(p); err != nil {
			continue
		}
		_ = os.Remove(p)
	}

	// The rotated set is measured against the budget minus the room the active
	// file is allowed to claim, because prune runs just after a rotation — when
	// the active file is empty and about to fill again. Charging it only its
	// current size would let the set drift back over budget between rotations,
	// which is the one thing MaxTotal exists to prevent.
	reserve := w.opt.MaxSize
	if w.size > reserve {
		reserve = w.size
	}
	budget := w.opt.MaxTotal - reserve
	if budget < 0 {
		budget = 0
	}

	type gen struct {
		path string
		size int64
		n    int
	}
	var kept []gen
	var total int64
	for i := 1; i <= w.opt.MaxFiles; i++ {
		p := generation(w.path, i)
		info, err := os.Stat(p)
		if err != nil {
			continue
		}
		kept = append(kept, gen{path: p, size: info.Size(), n: i})
		total += info.Size()
	}
	// Oldest (highest generation number) goes first.
	sort.Slice(kept, func(a, b int) bool { return kept[a].n > kept[b].n })
	for _, g := range kept {
		if total <= budget {
			return
		}
		if err := os.Remove(g.path); err == nil {
			total -= g.size
		}
	}
}

// Tail returns the last n lines written, newest last, walking back from the
// active file through the rotated generations until it has enough. A file that
// cannot be read is skipped rather than failing the whole call — a diagnostics
// bundle with a short log beats no bundle.
func (w *Writer) Tail(n int) []string {
	if n <= 0 {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()

	var out []string
	for i := 0; i <= w.opt.MaxFiles && len(out) < n; i++ {
		p := w.path
		if i > 0 {
			p = generation(w.path, i)
		}
		lines := tailFile(p, n-len(out))
		if len(lines) == 0 {
			continue
		}
		out = append(lines, out...)
	}
	if len(out) > n {
		out = out[len(out)-n:]
	}
	return out
}

// Close closes the active file. It is idempotent.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	err := w.f.Close()
	w.f = nil
	return err
}

// generation renders the name of the nth rotated file: "service.log" ->
// "service.log.1". Numbered suffixes rather than timestamps, so the newest
// rotated file always has the same name and a support instruction can say
// "attach service.log and service.log.1".
func generation(path string, n int) string { return fmt.Sprintf("%s.%d", path, n) }

// tailFile returns up to n trailing lines of path, reading at most tailWindow
// bytes from the end. A partial first line (the window landing mid-line) is
// dropped rather than reported truncated.
func tailFile(path string, n int) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil || info.Size() == 0 {
		return nil
	}
	size := info.Size()
	start := int64(0)
	if size > tailWindow {
		start = size - tailWindow
	}
	buf := make([]byte, size-start)
	if _, err := f.ReadAt(buf, start); err != nil && len(buf) == 0 {
		return nil
	}
	if start > 0 {
		if i := bytes.IndexByte(buf, '\n'); i >= 0 {
			buf = buf[i+1:]
		}
	}
	lines := []string{}
	for _, ln := range bytes.Split(bytes.TrimRight(buf, "\n"), []byte("\n")) {
		lines = append(lines, string(bytes.TrimRight(ln, "\r")))
	}
	if len(lines) == 1 && lines[0] == "" {
		return nil
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}
