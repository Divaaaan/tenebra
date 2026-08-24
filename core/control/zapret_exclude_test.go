package control

import (
	"encoding/binary"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Divaaaan/tenebra/core/model"
	"github.com/Divaaaan/tenebra/core/profile"
	"github.com/Divaaaan/tenebra/core/routing"
	"github.com/Divaaaan/tenebra/core/zapret"
)

// collectWarnings records every warning the daemon emits while fn runs.
func collectWarnings(t *testing.T, d *Daemon, fn func()) []string {
	t.Helper()
	return collectLogs(t, d, LogWarn, fn)
}

// collectLogs records the messages of one level the daemon emits while fn runs.
func collectLogs(t *testing.T, d *Daemon, level string, fn func()) []string {
	t.Helper()
	d.mu.Lock()
	d.logLevel = LogDebug // so a debug line reaches the emitter at all
	d.mu.Unlock()

	var got []string
	d.SetEmitter(func(name string, body any) {
		if name != EventLog {
			return
		}
		ev, ok := body.(LogEvent)
		if !ok {
			t.Fatalf("log event body = %T, want LogEvent", body)
		}
		if ev.Level == level {
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
	d.zapretExclude = func(string, []string, []zapret.Lookup) (zapret.ExcludeReport, error) {
		return zapret.ExcludeReport{Unresolved: []string{"de1.example.com", "nl2.example.com"}}, nil
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
	d.zapretExclude = func(string, []string, []zapret.Lookup) (zapret.ExcludeReport, error) {
		return zapret.ExcludeReport{}, nil
	}

	warnings := collectWarnings(t, d, func() {
		d.excludeNodesFromZapret(filepath.Join(d.store.Dir(), zapretDirName))
	})
	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none", warnings)
	}
}

// A node covered by the address it last answered with is protected, so it is not
// a warning — but it is running on a memory, and if the node has moved since,
// this line is the only thing that says which ones.
func TestExcludeNodesNotesTheNodesCoveredFromMemory(t *testing.T) {
	d, _ := newTestDaemon(t)
	d.zapretExclude = func(string, []string, []zapret.Lookup) (zapret.ExcludeReport, error) {
		return zapret.ExcludeReport{FromCache: []string{"de1.example.com"}}, nil
	}

	run := func() { d.excludeNodesFromZapret(filepath.Join(d.store.Dir(), zapretDirName)) }
	if warnings := collectWarnings(t, d, run); len(warnings) != 0 {
		t.Errorf("warnings = %v, want none: the node is covered", warnings)
	}
	debug := collectLogs(t, d, LogDebug, run)
	found := false
	for _, line := range debug {
		if strings.Contains(line, "de1.example.com") {
			found = true
		}
	}
	if !found {
		t.Errorf("debug log = %v, want the name covered from the last answer", debug)
	}
}

// A long list of dead names is trimmed rather than dumped: one warning must not
// push the rest of the log out of the buffer, and the count still says how many
// there were.
func TestExcludeNodesTrimsALongListOfNames(t *testing.T) {
	d, _ := newTestDaemon(t)
	names := []string{"a.example", "b.example", "c.example", "d.example", "e.example", "f.example", "g.example"}
	d.zapretExclude = func(string, []string, []zapret.Lookup) (zapret.ExcludeReport, error) {
		return zapret.ExcludeReport{Unresolved: names}, nil
	}

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

// The address that has to be excluded is the one sing-box will dial, and
// sing-box resolves an outbound server name through the direct resolver from the
// routing config — not through the machine's. Where the two disagree (a poisoned
// name, geo-DNS, round-robin) an exclusion built from the system answer alone
// covers an address nobody dials. So the configured resolver must actually be
// asked: here it is the only one holding the address, and the address has to
// come out in the file.
func TestExcludeNodesAsksTheResolverSingBoxWillUse(t *testing.T) {
	d, _ := newTestDaemon(t)
	// Only this resolver knows the name; the system one will answer .invalid with
	// NXDOMAIN, which is the point.
	addr := fakeDNSResolver(t, net.IPv4(198, 51, 100, 77))
	d.mu.Lock()
	d.routing.DNSDirect = "udp://" + addr
	d.mu.Unlock()
	d.zapretExclude = (&zapret.Excluder{Budget: 400 * time.Millisecond}).Exclude

	if err := d.store.Add(profile.Profile{
		ID: "p", Name: "P", Source: profile.SourceManual,
		Servers: []profile.Server{{ID: "n1", Node: model.Node{
			Name: "node", Protocol: model.VLESS, Server: "node.invalid", Port: 443,
		}}},
	}); err != nil {
		t.Fatalf("seed profile: %v", err)
	}

	dir := filepath.Join(d.store.Dir(), zapretDirName)
	d.excludeNodesFromZapret(dir)

	list, err := os.ReadFile(filepath.Join(dir, "lists", "ipset-exclude-user.txt"))
	if err != nil {
		t.Fatalf("read exclude list: %v", err)
	}
	if !strings.Contains(string(list), "198.51.100.77") {
		t.Errorf("the configured resolver was not the one asked:\n%s", list)
	}
}

// A resolver whose transport this client cannot speak (DoQ) must leave the
// system resolver doing the work rather than leaving the lookup with nothing.
func TestNodeLookupsFallBackWhenTheTransportIsUnknown(t *testing.T) {
	d, _ := newTestDaemon(t)
	if got := len(d.nodeLookups()); got != 2 {
		t.Fatalf("lookups = %d with the default resolver, want the system one and the configured one", got)
	}
	d.mu.Lock()
	d.routing.DNSDirect = "quic://dns.adguard.com"
	d.mu.Unlock()
	if got := len(d.nodeLookups()); got != 1 {
		t.Fatalf("lookups = %d for a transport we cannot speak, want just the system one", got)
	}
}

// ...and it says so. Dropping the configured resolver puts the node names back
// through the machine's resolver alone, which is the behaviour the exclusion list
// was fixed to stop having; without a line, a user on quic:// gets that behaviour
// back and nothing anywhere connects it to their DNS setting.
func TestNodeLookupsSaysWhenItCannotSpeakTheTransport(t *testing.T) {
	d, _ := newTestDaemon(t)
	d.mu.Lock()
	d.routing.DNSDirect = "quic://dns.adguard.com"
	d.mu.Unlock()

	debug := collectLogs(t, d, LogDebug, func() { d.nodeLookups() })
	found := false
	for _, line := range debug {
		if strings.Contains(line, "quic://dns.adguard.com") {
			found = true
		}
	}
	if !found {
		t.Errorf("debug log = %v, want the resolver that could not be used named", debug)
	}

	// The usable case stays quiet: a resolver that works is not news.
	d.mu.Lock()
	d.routing.DNSDirect = routing.DefaultDNSDirect
	d.mu.Unlock()
	if quiet := collectLogs(t, d, LogDebug, func() { d.nodeLookups() }); len(quiet) != 0 {
		t.Errorf("debug log = %v, want nothing when the configured resolver is usable", quiet)
	}
}

// fakeDNSResolver answers every query on loopback with one A record. The answer
// echoes the query's ID and points its record at the question with a
// compression pointer, which is the shape a real resolver sends.
func fakeDNSResolver(t *testing.T, ip net.IP) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen udp: %v", err)
	}
	t.Cleanup(func() { pc.Close() })

	go func() {
		buf := make([]byte, 4096)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			query := buf[:n]
			resp := append([]byte(nil), query...)
			binary.BigEndian.PutUint16(resp[2:4], 0x8180) // response, recursion available
			binary.BigEndian.PutUint16(resp[6:8], 1)      // one answer
			resp = append(resp, 0xC0, 0x0C)               // owner name -> the question
			resp = binary.BigEndian.AppendUint16(resp, 1) // type A
			resp = binary.BigEndian.AppendUint16(resp, 1) // class IN
			resp = binary.BigEndian.AppendUint32(resp, 60)
			resp = binary.BigEndian.AppendUint16(resp, net.IPv4len)
			resp = append(resp, ip.To4()...)
			pc.WriteTo(resp, from)
		}
	}()
	return pc.LocalAddr().String()
}
