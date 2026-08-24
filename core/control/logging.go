package control

import (
	"os"
	"strings"
	"sync"
	"time"
)

// LogLevelEnv names the environment variable that sets the daemon's log level.
// It is read once, at construction, so a support session is "set it, restart the
// core, reproduce, unset it" — deliberately not a persisted preference. A debug
// level someone turned on in January and forgot about is the same disease as an
// unrotated log: it costs disk and attention for nothing, and it is worse for
// being invisible.
const LogLevelEnv = "TENEBRA_LOG_LEVEL"

// DefaultLogLevel is what the daemon runs at unless told otherwise. Info, never
// debug: the default has to be the level a machine can run at for months.
const DefaultLogLevel = LogInfo

// logSeverities ranks the levels so a threshold can filter them. Anything not
// listed is treated as info, so an unrecognised level from a future build is
// shown rather than silently dropped.
var logSeverities = map[string]int{
	LogDebug: 0,
	LogInfo:  1,
	LogWarn:  2,
	LogError: 3,
}

// logSeverity ranks one level, defaulting unknown values to info.
func logSeverity(level string) int {
	if s, ok := logSeverities[level]; ok {
		return s
	}
	return logSeverities[LogInfo]
}

// ParseLogLevel maps a user-supplied string onto a level constant. It accepts
// the level names case-insensitively plus the common spellings people reach for
// (warning, err, trace), and reports whether it recognised the input, so a
// caller can tell "the user asked for debug" from "the user typed nonsense" and
// keep the default in the second case rather than silently running at trace.
func ParseLogLevel(s string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug", "trace", "verbose":
		return LogDebug, true
	case "info", "information", "notice":
		return LogInfo, true
	case "warn", "warning":
		return LogWarn, true
	case "error", "err", "fatal":
		return LogError, true
	default:
		return DefaultLogLevel, false
	}
}

// logLevelFromEnv resolves the starting level from the environment, falling back
// to the default when the variable is unset or unreadable.
func logLevelFromEnv() string {
	raw := os.Getenv(LogLevelEnv)
	if raw == "" {
		return DefaultLogLevel
	}
	level, _ := ParseLogLevel(raw)
	return level
}

// SetLogLevel raises or lowers the threshold below which log lines are dropped.
// An unrecognised level is ignored and reported, so a bad value never silences
// the daemon. Safe to call while serving.
func (d *Daemon) SetLogLevel(level string) bool {
	parsed, ok := ParseLogLevel(level)
	if !ok {
		return false
	}
	d.mu.Lock()
	d.logLevel = parsed
	d.mu.Unlock()
	return true
}

// LogLevel reports the current threshold.
func (d *Daemon) LogLevel() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.logLevel
}

// SetLogSink installs a second destination for every log line that passes the
// level filter — in production the process logger, which in service mode is the
// rotating file on disk.
//
// Without it the daemon's whole narrative of a connect exists only for as long
// as a UI happens to be attached to receive it. That is exactly backwards: the
// failures worth reading about are the ones that happen at boot, on a machine
// nobody is looking at, where the service reconnects on its own and gives up
// before anyone opens the app. Call it before serving.
func (d *Daemon) SetLogSink(sink func(level, msg string)) {
	d.mu.Lock()
	d.logSink = sink
	d.mu.Unlock()
}

// SetLogTail installs a reader for the trailing lines of the process log file,
// used by the diagnostics bundle. nil (the default, and the case for every mode
// that logs to stderr rather than a file) simply leaves that section of the
// bundle to the in-memory ring.
func (d *Daemon) SetLogTail(tail func(n int) []string) {
	d.mu.Lock()
	d.logTail = tail
	d.mu.Unlock()
}

// logRingSize is how many recent log lines the daemon keeps in memory for the
// diagnostics bundle. A few hundred covers a whole connect walk with its probe
// results and still costs well under a megabyte.
const logRingSize = 400

// logEntry is one retained log line.
type logEntry struct {
	At    time.Time
	Level string
	Msg   string
}

// logRing is a fixed-size circular buffer of the most recent log lines. It is
// what lets the daemon answer "what happened just before this failed" without a
// UI having been attached at the time, and without a file on platforms that
// have none.
type logRing struct {
	mu   sync.Mutex
	buf  []logEntry
	next int
	full bool
}

func newLogRing(size int) *logRing {
	if size < 1 {
		size = 1
	}
	return &logRing{buf: make([]logEntry, size)}
}

// add records one line, overwriting the oldest when the ring is full.
func (r *logRing) add(e logEntry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf[r.next] = e
	r.next = (r.next + 1) % len(r.buf)
	if r.next == 0 {
		r.full = true
	}
}

// snapshot returns the retained lines oldest first.
func (r *logRing) snapshot() []logEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.full {
		out := make([]logEntry, r.next)
		copy(out, r.buf[:r.next])
		return out
	}
	out := make([]logEntry, 0, len(r.buf))
	out = append(out, r.buf[r.next:]...)
	out = append(out, r.buf[:r.next]...)
	return out
}
