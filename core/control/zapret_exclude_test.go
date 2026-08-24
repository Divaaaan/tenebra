package control

import (
	"path/filepath"
	"strings"
	"testing"
)

// collectWarnings records every warning the daemon emits while fn runs.
func collectWarnings(t *testing.T, d *Daemon, fn func()) []string {
	t.Helper()
	var got []string
	d.SetEmitter(func(name string, body any) {
		if name != EventLog {
			return
		}
		ev, ok := body.(LogEvent)
		if !ok {
			t.Fatalf("log event body = %T, want LogEvent", body)
		}
		if ev.Level == LogWarn {
			got = append(got, ev.Msg)
		}
	})
	fn()
	d.SetEmitter(nil)
	return got
}

// A node the exclusion list could not cover is the failure the list exists to
// prevent — the filter desyncs the handshake to our own node and the user reads
// it as a dead node. It has to reach the log by name, or the only trace is a
// tunnel that connects and carries nothing.
func TestExcludeNodesWarnsAboutNamesThatDidNotResolve(t *testing.T) {
	d, _ := newTestDaemon(t)
	d.zapretExclude = func(string, []string) ([]string, error) {
		return []string{"de1.example.com", "nl2.example.com"}, nil
	}

	warnings := collectWarnings(t, d, func() {
		d.excludeNodesFromZapret(filepath.Join(d.store.Dir(), zapretDirName))
	})

	if len(warnings) != 1 {
		t.Fatalf("warnings = %v, want exactly one", warnings)
	}
	for _, want := range []string{"de1.example.com", "nl2.example.com", "2"} {
		if !strings.Contains(warnings[0], want) {
			t.Errorf("warning %q does not mention %q", warnings[0], want)
		}
	}
}

// The quiet path stays quiet: every node covered is not news, and a line per
// connect would train the user to ignore the one that matters.
func TestExcludeNodesSaysNothingWhenEveryNodeResolved(t *testing.T) {
	d, _ := newTestDaemon(t)
	d.zapretExclude = func(string, []string) ([]string, error) { return nil, nil }

	warnings := collectWarnings(t, d, func() {
		d.excludeNodesFromZapret(filepath.Join(d.store.Dir(), zapretDirName))
	})
	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none", warnings)
	}
}

// A long list of dead names is trimmed rather than dumped: one warning must not
// push the rest of the log out of the buffer, and the count still says how many
// there were.
func TestExcludeNodesTrimsALongListOfNames(t *testing.T) {
	d, _ := newTestDaemon(t)
	names := []string{"a.example", "b.example", "c.example", "d.example", "e.example", "f.example", "g.example"}
	d.zapretExclude = func(string, []string) ([]string, error) { return names, nil }

	warnings := collectWarnings(t, d, func() {
		d.excludeNodesFromZapret(filepath.Join(d.store.Dir(), zapretDirName))
	})
	if len(warnings) != 1 {
		t.Fatalf("warnings = %v, want exactly one", warnings)
	}
	if strings.Contains(warnings[0], "g.example") {
		t.Errorf("the warning spelled out every name:\n%s", warnings[0])
	}
	for _, want := range []string{"a.example", "(7)", "и ещё 2"} {
		if !strings.Contains(warnings[0], want) {
			t.Errorf("warning %q does not mention %q", warnings[0], want)
		}
	}
}
