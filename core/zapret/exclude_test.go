package zapret

import (
	"context"
	"encoding/json"
	"fmt"
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

// table is a resolver that answers from a fixed table. A name missing from it
// answers the way a dead name does. Nothing here reaches a resolver: the point
// under test is what the file ends up containing, not what the machine's DNS
// happens to say today.
func table(answers map[string][]string) Lookup {
	return func(_ context.Context, host string) ([]net.IP, error) {
		addrs, ok := answers[host]
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

// dead is a resolver that is there but never answers, until the deadline.
func dead(_ context.Context, _ string) ([]net.IP, error) {
	return nil, &net.DNSError{Err: "server misbehaving", IsTemporary: true}
}

// stalls is a resolver that holds the call open until its context ends.
func stalls(ctx context.Context, _ string) ([]net.IP, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// The exclusion is what stops the bypass from desyncing the tunnel's own
// handshake: the exit node is served on one of the very ports winws watches, and
// a mangled ClientHello there looks exactly like a dead node.
func TestExcludeNodesListsNodeAddresses(t *testing.T) {
	dir := t.TempDir()
	e := &Excluder{}
	lookups := []Lookup{table(map[string][]string{
		"node.example.com": {"198.51.100.10", "2001:db8::10"},
	})}

	report, err := e.Exclude(dir, []string{"95.163.176.178", "[2001:db8::1]", "node.example.com", ""}, lookups)
	if err != nil {
		t.Fatalf("Exclude: %v", err)
	}
	if len(report.Unresolved) != 0 {
		t.Fatalf("unresolved = %v, want none", report.Unresolved)
	}

	got := readExclude(t, dir)
	if !strings.Contains(got, "95.163.176.178") {
		t.Error("the node address was not excluded")
	}
	if !strings.Contains(got, "2001:db8::1\r\n") {
		t.Error("a bracketed IPv6 node address was not excluded")
	}
	// A node given by name is the ordinary case in a subscription, and it is the
	// one that used to fall out of the list without a trace. Every address it
	// answers with is written: any of them is one the tunnel may dial.
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

// The whole reason a list of resolvers is passed: the address the tunnel dials
// is whatever ITS resolver returns, and two resolvers disagree exactly where it
// matters — a poisoned system answer against an honest encrypted one, or a
// geo-DNS handing each caller a different record. Both answers go in; excluding
// an address the tunnel does not use costs nothing.
func TestExcludeNodesWritesEveryResolversAnswer(t *testing.T) {
	dir := t.TempDir()
	e := &Excluder{}
	system := table(map[string][]string{"node.example.com": {"198.51.100.10"}})
	direct := table(map[string][]string{"node.example.com": {"203.0.113.77"}})

	report, err := e.Exclude(dir, []string{"node.example.com"}, []Lookup{system, direct})
	if err != nil {
		t.Fatalf("Exclude: %v", err)
	}
	if len(report.Unresolved) != 0 {
		t.Fatalf("unresolved = %v, want none", report.Unresolved)
	}
	got := readExclude(t, dir)
	for _, want := range []string{"198.51.100.10", "203.0.113.77"} {
		if !strings.Contains(got, want) {
			t.Errorf("%s was not excluded — one resolver's answer was dropped:\n%s", want, got)
		}
	}
}

// A name that will not resolve leaves that node unprotected, which is the exact
// failure this file exists to prevent. It has to come back as a number and a
// name — silence here is what sent users hunting through their subscription for
// a fault that was on their own machine.
func TestExcludeNodesReportsNamesItCouldNotResolve(t *testing.T) {
	dir := t.TempDir()
	e := &Excluder{}

	report, err := e.Exclude(dir, []string{"95.163.176.178", "dead.example.com"}, []Lookup{table(nil)})
	if err != nil {
		t.Fatalf("Exclude: %v", err)
	}
	if len(report.Unresolved) != 1 || report.Unresolved[0] != "dead.example.com" {
		t.Fatalf("unresolved = %v, want [dead.example.com]", report.Unresolved)
	}
	// One dead name must not cost the other nodes their exclusion.
	if got := readExclude(t, dir); !strings.Contains(got, "95.163.176.178") {
		t.Errorf("the addresses that did resolve were dropped too:\n%s", got)
	}
}

// The caller builds the resolver list conditionally, so it can hand over a gap.
// A nil call inside a goroutine is a panic no recover catches, and it would take
// the daemon — and the user's tunnel — down with it.
func TestExcludeNodesSurvivesAGapInTheResolverList(t *testing.T) {
	dir := t.TempDir()
	lookups := []Lookup{nil, table(map[string][]string{"node.example.com": {"198.51.100.10"}}), nil}

	report, err := (&Excluder{}).Exclude(dir, []string{"node.example.com"}, lookups)
	if err != nil {
		t.Fatalf("Exclude: %v", err)
	}
	if len(report.Unresolved) != 0 {
		t.Fatalf("unresolved = %v, want none", report.Unresolved)
	}
	if got := readExclude(t, dir); !strings.Contains(got, "198.51.100.10") {
		t.Errorf("the resolver that was there did not get used:\n%s", got)
	}
}

// A real subscription mixes literals and names, sometimes the same node twice.
// The whole list has to survive the trip, deduplicated, with only the genuinely
// dead names left over.
func TestExcludeNodesTakesTheWholeMix(t *testing.T) {
	dir := t.TempDir()
	e := &Excluder{}
	lookups := []Lookup{table(map[string][]string{
		"a.example.com": {"198.51.100.10", "198.51.100.11"},
		"b.example.com": {"198.51.100.11", "2001:db8::b"},
	})}

	report, err := e.Exclude(dir, []string{
		"95.163.176.178",
		"a.example.com",
		"[2001:db8::1]",
		"A.example.com", // the same name, cased differently
		"b.example.com",
		"dead.example.com",
		"",
	}, lookups)
	if err != nil {
		t.Fatalf("Exclude: %v", err)
	}
	if len(report.Unresolved) != 1 || report.Unresolved[0] != "dead.example.com" {
		t.Fatalf("unresolved = %v, want [dead.example.com]", report.Unresolved)
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
	e := &Excluder{}
	lookups := []Lookup{table(map[string][]string{
		"hijacked.example.com": {"0.0.0.0", "127.0.0.1"},
	})}

	report, err := e.Exclude(dir, []string{"hijacked.example.com"}, lookups)
	if err != nil {
		t.Fatalf("Exclude: %v", err)
	}
	if len(report.Unresolved) != 1 || report.Unresolved[0] != "hijacked.example.com" {
		t.Fatalf("unresolved = %v, want [hijacked.example.com]", report.Unresolved)
	}
	if got := readExclude(t, dir); strings.Contains(got, "0.0.0.0") || strings.Contains(got, "127.0.0.1") {
		t.Errorf("a hijacked answer was written as a node address:\n%s", got)
	}
}

// The failure this protects against: DNS was fine when the node was first
// excluded and is not fine now. Rewriting the list from only what resolves this
// second would drop the address and hand the node back to the filter — the
// original symptom, returning silently, in exactly the conditions the exclusion
// is for.
func TestExcludeNodesKeepsTheLastKnownAddressWhenDNSFails(t *testing.T) {
	dir := t.TempDir()
	e := &Excluder{Budget: 200 * time.Millisecond}
	live := []Lookup{table(map[string][]string{"node.example.com": {"198.51.100.10"}})}

	if _, err := e.Exclude(dir, []string{"node.example.com"}, live); err != nil {
		t.Fatalf("Exclude: %v", err)
	}

	report, err := e.Exclude(dir, []string{"node.example.com"}, []Lookup{dead})
	if err != nil {
		t.Fatalf("Exclude with a broken resolver: %v", err)
	}
	if len(report.Unresolved) != 0 {
		t.Fatalf("unresolved = %v, want none: the address was known", report.Unresolved)
	}
	// Confirmed seconds ago, so writing it is unremarkable — saying so on every
	// connect would bury the case that matters, which the next test covers.
	if len(report.FromCache) != 0 {
		t.Fatalf("fromCache = %v, want none for an address just confirmed", report.FromCache)
	}
	if got := readExclude(t, dir); !strings.Contains(got, "198.51.100.10") {
		t.Errorf("a DNS outage erased the exclusion:\n%s", got)
	}
}

// An address DNS has not agreed with for hours is still written — better an old
// address than none — but if the node has moved since, this is the only clue
// anyone gets, so it has to be said.
func TestExcludeNodesNamesTheNodesRunningOnAnOldAnswer(t *testing.T) {
	dir := t.TempDir()
	lists := filepath.Join(dir, "lists")
	if err := os.MkdirAll(lists, 0o755); err != nil {
		t.Fatal(err)
	}
	stale, err := json.Marshal(cacheFile{Version: 1, Hosts: map[string]cacheEntry{
		"node.example.com": {Addrs: []string{"198.51.100.10"}, Seen: time.Now().Add(-3 * time.Hour)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lists, nodeCacheFile), stale, 0o644); err != nil {
		t.Fatal(err)
	}

	report, err := (&Excluder{}).Exclude(dir, []string{"node.example.com"}, []Lookup{dead})
	if err != nil {
		t.Fatalf("Exclude: %v", err)
	}
	if len(report.FromCache) != 1 || report.FromCache[0] != "node.example.com" {
		t.Fatalf("fromCache = %v, want [node.example.com]", report.FromCache)
	}
	if len(report.Unresolved) != 0 {
		t.Fatalf("unresolved = %v, want none: the node is covered", report.Unresolved)
	}
	if got := readExclude(t, dir); !strings.Contains(got, "198.51.100.10") {
		t.Errorf("the remembered address was not written:\n%s", got)
	}
}

// The memory is on disk, so it also survives the client being restarted — and a
// bundle update, which carries the file across (see userFiles).
func TestExcludeNodesRemembersAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	live := []Lookup{table(map[string][]string{"node.example.com": {"198.51.100.10"}})}
	if _, err := (&Excluder{}).Exclude(dir, []string{"node.example.com"}, live); err != nil {
		t.Fatalf("Exclude: %v", err)
	}

	// A different Excluder over the same bundle is what the next process start
	// looks like.
	report, err := (&Excluder{}).Exclude(dir, []string{"node.example.com"}, []Lookup{dead})
	if err != nil {
		t.Fatalf("Exclude after restart: %v", err)
	}
	if len(report.Unresolved) != 0 {
		t.Fatalf("unresolved = %v, want none: the address was on disk", report.Unresolved)
	}
	if got := readExclude(t, dir); !strings.Contains(got, "198.51.100.10") {
		t.Errorf("the remembered address did not survive a restart:\n%s", got)
	}
}

// An address remembered for a node dropped from every profile must not be
// written back: the file would keep protecting a node that is gone.
func TestExcludeNodesForgetsNodesNoLongerConfigured(t *testing.T) {
	dir := t.TempDir()
	e := &Excluder{}
	live := []Lookup{table(map[string][]string{
		"old.example.com": {"198.51.100.10"},
		"new.example.com": {"203.0.113.5"},
	})}

	if _, err := e.Exclude(dir, []string{"old.example.com"}, live); err != nil {
		t.Fatalf("Exclude: %v", err)
	}
	if _, err := e.Exclude(dir, []string{"new.example.com"}, live); err != nil {
		t.Fatalf("Exclude: %v", err)
	}
	got := readExclude(t, dir)
	if strings.Contains(got, "198.51.100.10") {
		t.Errorf("the address of a node no longer configured was written back:\n%s", got)
	}
	if !strings.Contains(got, "203.0.113.5") {
		t.Errorf("the current node was not excluded:\n%s", got)
	}
}

// The remembered set is bounded: profiles come and go, and a file that only ever
// grows would eventually be read and written on every connect for no reason.
func TestExcludeNodesBoundsWhatItRemembers(t *testing.T) {
	dir := t.TempDir()
	answers := make(map[string][]string, maxCacheEntries+64)
	servers := make([]string, 0, maxCacheEntries+64)
	for i := 0; i < maxCacheEntries+64; i++ {
		host := fmt.Sprintf("node%d.example.com", i)
		answers[host] = []string{fmt.Sprintf("198.51.100.%d", i%256)}
		servers = append(servers, host)
	}

	if _, err := (&Excluder{}).Exclude(dir, servers, []Lookup{table(answers)}); err != nil {
		t.Fatalf("Exclude: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "lists", nodeCacheFile))
	if err != nil {
		t.Fatalf("read cache: %v", err)
	}
	var f cacheFile
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatalf("parse cache: %v", err)
	}
	if len(f.Hosts) > maxCacheEntries {
		t.Errorf("remembered %d names, cap is %d", len(f.Hosts), maxCacheEntries)
	}
}

// This runs on the connect path, in front of the bypass. A resolver that never
// answers must cost a bounded pause and then a warning, not the connect.
func TestExcludeNodesDoesNotStallTheConnect(t *testing.T) {
	dir := t.TempDir()
	e := &Excluder{Budget: 100 * time.Millisecond}

	start := time.Now()
	report, err := e.Exclude(dir, []string{"slow.example.com", "95.163.176.178"}, []Lookup{stalls})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Exclude: %v", err)
	}
	if elapsed > time.Second {
		t.Errorf("the write waited %v on a dead resolver, budget is %v", elapsed, e.Budget)
	}
	if len(report.Unresolved) != 1 || report.Unresolved[0] != "slow.example.com" {
		t.Fatalf("unresolved = %v, want [slow.example.com]", report.Unresolved)
	}
	if got := readExclude(t, dir); !strings.Contains(got, "95.163.176.178") {
		t.Errorf("a timed-out name took the literal addresses down with it:\n%s", got)
	}
}

// The budget belongs to the Excluder, not to the package: two of them with
// different budgets must behave differently at the same moment, or the value is
// global state wearing a field's clothes.
func TestExcludeBudgetIsPerExcluder(t *testing.T) {
	short := &Excluder{Budget: 50 * time.Millisecond}
	long := &Excluder{Budget: 600 * time.Millisecond}

	start := time.Now()
	if _, err := short.Exclude(t.TempDir(), []string{"slow.example.com"}, []Lookup{stalls}); err != nil {
		t.Fatalf("Exclude: %v", err)
	}
	shortElapsed := time.Since(start)

	start = time.Now()
	if _, err := long.Exclude(t.TempDir(), []string{"slow.example.com"}, []Lookup{stalls}); err != nil {
		t.Fatalf("Exclude: %v", err)
	}
	longElapsed := time.Since(start)

	if shortElapsed > 400*time.Millisecond {
		t.Errorf("the short budget waited %v, budget is %v", shortElapsed, short.Budget)
	}
	if longElapsed < long.Budget {
		t.Errorf("the long budget gave up after %v, budget is %v", longElapsed, long.Budget)
	}
}

// The budget has to hold for a resolver that ignores its context too — the
// lookups are handed in by the caller, and the guarantee this makes to the
// connect path cannot rest on what is plugged into it.
func TestExcludeNodesGivesUpOnAResolverThatIgnoresTheDeadline(t *testing.T) {
	dir := t.TempDir()
	e := &Excluder{Budget: 50 * time.Millisecond}
	deaf := Lookup(func(context.Context, string) ([]net.IP, error) {
		time.Sleep(500 * time.Millisecond) // deaf to cancellation
		return []net.IP{net.ParseIP("198.51.100.10")}, nil
	})

	start := time.Now()
	report, err := e.Exclude(dir, []string{"deaf.example.com"}, []Lookup{deaf})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Exclude: %v", err)
	}
	if elapsed > 300*time.Millisecond {
		t.Errorf("the write waited %v on a resolver deaf to its context, budget is %v", elapsed, e.Budget)
	}
	if len(report.Unresolved) != 1 || report.Unresolved[0] != "deaf.example.com" {
		t.Fatalf("unresolved = %v, want [deaf.example.com]", report.Unresolved)
	}
}

// A name whose address is already known must not hold the connect back at all:
// there is something to write either way, and the refresh can land in the file
// on the next connect. This is what keeps the cost off every reconnect.
func TestExcludeNodesDoesNotWaitForNamesItAlreadyKnows(t *testing.T) {
	dir := t.TempDir()
	e := &Excluder{Budget: 2 * time.Second}
	live := []Lookup{table(map[string][]string{"node.example.com": {"198.51.100.10"}})}
	if _, err := e.Exclude(dir, []string{"node.example.com"}, live); err != nil {
		t.Fatalf("Exclude: %v", err)
	}

	start := time.Now()
	report, err := e.Exclude(dir, []string{"node.example.com"}, []Lookup{stalls})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Exclude: %v", err)
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("a known name still cost %v of the %v budget", elapsed, e.Budget)
	}
	if len(report.Unresolved) != 0 {
		t.Errorf("unresolved = %v, want none: the address was known", report.Unresolved)
	}
	if got := readExclude(t, dir); !strings.Contains(got, "198.51.100.10") {
		t.Errorf("the known address was not written:\n%s", got)
	}
}

// A name that is simply gone — a node decommissioned upstream, still in the
// user's profile — cost the full budget on every connect. It is still asked
// after, but it stops being something the connect waits for.
func TestExcludeNodesStopsPayingForADeadNameEveryTime(t *testing.T) {
	dir := t.TempDir()
	e := &Excluder{Budget: 300 * time.Millisecond, RetryAfter: time.Hour}

	start := time.Now()
	if _, err := e.Exclude(dir, []string{"gone.example.com"}, []Lookup{stalls}); err != nil {
		t.Fatalf("Exclude: %v", err)
	}
	if first := time.Since(start); first < e.Budget {
		t.Fatalf("the first attempt gave up after %v, want the full %v", first, e.Budget)
	}

	start = time.Now()
	report, err := e.Exclude(dir, []string{"gone.example.com"}, []Lookup{stalls})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Exclude: %v", err)
	}
	if elapsed > 100*time.Millisecond {
		t.Errorf("the second attempt spent %v on the same dead name", elapsed)
	}
	// Not waiting is not the same as not saying: the node is still exposed.
	if len(report.Unresolved) != 1 || report.Unresolved[0] != "gone.example.com" {
		t.Fatalf("unresolved = %v, want [gone.example.com]", report.Unresolved)
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

	e := &Excluder{}
	if _, err := e.Exclude(dir, []string{"95.163.176.178"}, nil); err != nil {
		t.Fatalf("Exclude: %v", err)
	}
	if _, err := e.Exclude(dir, []string{"107.189.22.68"}, nil); err != nil {
		t.Fatalf("Exclude rewrite: %v", err)
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
