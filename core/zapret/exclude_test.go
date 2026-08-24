package zapret

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func readExclude(t *testing.T, dir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "lists", excludeFile))
	if err != nil {
		t.Fatalf("read exclude list: %v", err)
	}
	return string(data)
}

// stubResolver points the package's lookup at a fixed table for one test. A name
// missing from the table answers the way a dead name does. Nothing here reaches a
// resolver: the point under test is what the file ends up containing, not what
// the machine's DNS happens to say today.
func stubResolver(t *testing.T, table map[string][]string) {
	t.Helper()
	prev := lookupIP
	t.Cleanup(func() { lookupIP = prev })
	lookupIP = func(_ context.Context, host string) ([]net.IP, error) {
		addrs, ok := table[host]
		if !ok {
			return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
		}
		out := make([]net.IP, 0, len(addrs))
		for _, a := range addrs {
			out = append(out, net.ParseIP(a))
		}
		return out, nil
	}
}

// The exclusion is what stops the bypass from desyncing the tunnel's own
// handshake: the exit node is served on one of the very ports winws watches, and
// a mangled ClientHello there looks exactly like a dead node.
func TestExcludeNodesListsNodeAddresses(t *testing.T) {
	dir := t.TempDir()
	stubResolver(t, map[string][]string{
		"node.example.com": {"198.51.100.10", "2001:db8::10"},
	})

	unresolved, err := ExcludeNodes(dir, []string{"95.163.176.178", "[2001:db8::1]", "node.example.com", ""})
	if err != nil {
		t.Fatalf("ExcludeNodes: %v", err)
	}
	if len(unresolved) != 0 {
		t.Fatalf("unresolved = %v, want none", unresolved)
	}

	got := readExclude(t, dir)
	if !strings.Contains(got, "95.163.176.178") {
		t.Error("the node address was not excluded")
	}
	if !strings.Contains(got, "2001:db8::1\r\n") {
		t.Error("a bracketed IPv6 node address was not excluded")
	}
	// A node given by name is the ordinary case in a subscription, and it is the
	// one that used to fall out of the list without a trace.
	for _, want := range []string{"198.51.100.10", "2001:db8::10"} {
		if !strings.Contains(got, want) {
			t.Errorf("address %s of a node given by name was not excluded:\n%s", want, got)
		}
	}
	// The name itself is not written: winws documents --ipset-exclude as one
	// ip/CIDR per line and answers anything else with "bad ip or subnet", so a
	// hostname in this file is a discarded line, not a deferred lookup.
	if strings.Contains(got, "node.example.com") {
		t.Error("a hostname was written into an ip-only list")
	}
}

// A name that will not resolve leaves that node unprotected, which is the exact
// failure this file exists to prevent. It has to come back as a number and a
// name — silence here is what sent users hunting through their subscription for
// a fault that was on their own machine.
func TestExcludeNodesReportsNamesItCouldNotResolve(t *testing.T) {
	dir := t.TempDir()
	stubResolver(t, nil)

	unresolved, err := ExcludeNodes(dir, []string{"95.163.176.178", "dead.example.com"})
	if err != nil {
		t.Fatalf("ExcludeNodes: %v", err)
	}
	if len(unresolved) != 1 || unresolved[0] != "dead.example.com" {
		t.Fatalf("unresolved = %v, want [dead.example.com]", unresolved)
	}
	// One dead name must not cost the other nodes their exclusion.
	if got := readExclude(t, dir); !strings.Contains(got, "95.163.176.178") {
		t.Errorf("the addresses that did resolve were dropped too:\n%s", got)
	}
}

// A real subscription mixes literals and names, sometimes the same node twice.
// The whole list has to survive the trip, deduplicated, with only the genuinely
// dead names left over.
func TestExcludeNodesTakesTheWholeMix(t *testing.T) {
	dir := t.TempDir()
	stubResolver(t, map[string][]string{
		"a.example.com": {"198.51.100.10", "198.51.100.11"},
		"b.example.com": {"198.51.100.11", "2001:db8::b"},
	})

	unresolved, err := ExcludeNodes(dir, []string{
		"95.163.176.178",
		"a.example.com",
		"[2001:db8::1]",
		"A.example.com", // the same name, cased differently
		"b.example.com",
		"dead.example.com",
		"",
	})
	if err != nil {
		t.Fatalf("ExcludeNodes: %v", err)
	}
	if len(unresolved) != 1 || unresolved[0] != "dead.example.com" {
		t.Fatalf("unresolved = %v, want [dead.example.com]", unresolved)
	}

	got := readExclude(t, dir)
	for _, want := range []string{
		"95.163.176.178", "2001:db8::1", "198.51.100.10", "198.51.100.11", "2001:db8::b",
	} {
		if strings.Count(got, want+"\r\n") != 1 {
			t.Errorf("%s appears %d times, want once:\n%s", want, strings.Count(got, want+"\r\n"), got)
		}
	}
}

// Resolvers that answer a dead name with 0.0.0.0 or the loopback are common
// enough that taking their word would write a line no node can be behind, while
// reporting the name as handled — the worst of both.
func TestExcludeNodesRejectsHijackedAnswers(t *testing.T) {
	dir := t.TempDir()
	stubResolver(t, map[string][]string{
		"hijacked.example.com": {"0.0.0.0", "127.0.0.1"},
	})

	unresolved, err := ExcludeNodes(dir, []string{"hijacked.example.com"})
	if err != nil {
		t.Fatalf("ExcludeNodes: %v", err)
	}
	if len(unresolved) != 1 || unresolved[0] != "hijacked.example.com" {
		t.Fatalf("unresolved = %v, want [hijacked.example.com]", unresolved)
	}
	if got := readExclude(t, dir); strings.Contains(got, "0.0.0.0") || strings.Contains(got, "127.0.0.1") {
		t.Errorf("a hijacked answer was written as a node address:\n%s", got)
	}
}

// This runs on the connect path, in front of the bypass. A resolver that never
// answers must cost a bounded pause and then a warning, not the connect.
func TestExcludeNodesDoesNotStallTheConnect(t *testing.T) {
	dir := t.TempDir()

	prevBudget := resolveBudget
	resolveBudget = 100 * time.Millisecond
	t.Cleanup(func() { resolveBudget = prevBudget })

	prev := lookupIP
	t.Cleanup(func() { lookupIP = prev })
	lookupIP = func(ctx context.Context, _ string) ([]net.IP, error) {
		<-ctx.Done() // a resolver that is there but never answers
		return nil, ctx.Err()
	}

	start := time.Now()
	unresolved, err := ExcludeNodes(dir, []string{"slow.example.com", "95.163.176.178"})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("ExcludeNodes: %v", err)
	}
	if elapsed > time.Second {
		t.Errorf("the write waited %v on a dead resolver, budget is %v", elapsed, resolveBudget)
	}
	if len(unresolved) != 1 || unresolved[0] != "slow.example.com" {
		t.Fatalf("unresolved = %v, want [slow.example.com]", unresolved)
	}
	if got := readExclude(t, dir); !strings.Contains(got, "95.163.176.178") {
		t.Errorf("a timed-out name took the literal addresses down with it:\n%s", got)
	}
}

// The file is user-editable and the bundle ships it for exactly that purpose, so
// rewriting it must not eat what the user put there.
func TestExcludeNodesKeepsUserEntries(t *testing.T) {
	dir := t.TempDir()
	lists := filepath.Join(dir, "lists")
	if err := os.MkdirAll(lists, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lists, excludeFile), []byte("# mine\r\n203.0.113.7\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := ExcludeNodes(dir, []string{"95.163.176.178"}); err != nil {
		t.Fatalf("ExcludeNodes: %v", err)
	}
	if _, err := ExcludeNodes(dir, []string{"107.189.22.68"}); err != nil {
		t.Fatalf("ExcludeNodes rewrite: %v", err)
	}

	got := readExclude(t, dir)
	if !strings.Contains(got, "203.0.113.7") || !strings.Contains(got, "# mine") {
		t.Errorf("user entries were lost:\n%s", got)
	}
	if !strings.Contains(got, "107.189.22.68") {
		t.Error("the current node list was not written")
	}
	// The managed block is replaced, not appended to, so a node dropped from the
	// subscription stops being excluded.
	if strings.Contains(got, "95.163.176.178") {
		t.Error("a stale node address survived the rewrite")
	}
	if strings.Count(got, excludeHeader) != 1 {
		t.Errorf("the managed header was duplicated:\n%s", got)
	}
}
