package dnspick

import "testing"

func doh(name string, rtt int64, ok bool) Result {
	return Result{Candidate: Candidate{Name: name, Address: "https://" + name, Kind: KindDoH}, OK: ok, RTTMs: rtt}
}

func plain(name string, rtt int64, ok bool) Result {
	return Result{Candidate: Candidate{Name: name, Address: name, Kind: KindPlain}, OK: ok, RTTMs: rtt}
}

// The rule the whole package turns on: on a filtered line the plain resolver is
// usually the FASTEST responder, because the answer came from a middlebox a few
// milliseconds away instead of the real server. Preferring latency alone would
// pick the tamperer every time.
func TestEncryptedWinsOverFasterPlain(t *testing.T) {
	results := []Result{
		plain("isp-resolver", 3, true),
		doh("adguard", 1470, true),
	}
	best, found := Best(results)
	if !found {
		t.Fatal("nothing picked")
	}
	if best.Kind != KindDoH {
		t.Fatalf("picked %s (%s), want the encrypted resolver despite being slower", best.Name, best.Kind)
	}
}

func TestBrokenResolversNeverWin(t *testing.T) {
	// Mirrors the author's ISP: Cloudflare and Google DoH time out, AdGuard works.
	results := []Result{
		doh("cloudflare", 0, false),
		doh("google", 0, false),
		doh("adguard", 1470, true),
	}
	best, found := Best(results)
	if !found || best.Name != "adguard" {
		t.Fatalf("picked %q (found=%v), want adguard", best.Name, found)
	}
}

func TestFasterEncryptedWinsAmongEncrypted(t *testing.T) {
	results := []Result{
		doh("slow", 900, true),
		doh("fast", 120, true),
	}
	best, _ := Best(results)
	if best.Name != "fast" {
		t.Fatalf("picked %q, want the faster encrypted resolver", best.Name)
	}
}

func TestPlainIsUsedOnlyWhenEverythingEncryptedIsBlocked(t *testing.T) {
	results := []Result{
		doh("cloudflare", 0, false),
		doh("adguard", 0, false),
		plain("yandex", 3, true),
	}
	best, found := Best(results)
	if !found || best.Kind != KindPlain {
		t.Fatalf("picked %q/%s, want the plain fallback", best.Name, best.Kind)
	}
}

// Reporting "none" beats silently keeping a resolver we just measured as broken:
// that would produce a client that reports success and resolves nothing.
func TestNoWorkingResolverIsReported(t *testing.T) {
	results := []Result{doh("a", 0, false), plain("b", 0, false)}
	if _, found := Best(results); found {
		t.Fatal("picked a resolver when every candidate failed")
	}
	if _, found := Best(nil); found {
		t.Fatal("picked a resolver from an empty set")
	}
}

func TestRankIsStableAndPure(t *testing.T) {
	in := []Result{doh("first", 100, true), doh("second", 100, true)}
	got := Rank(in)
	if got[0].Candidate.Name != "first" {
		t.Fatalf("equal candidates reordered: %s first", got[0].Candidate.Name)
	}
	if in[0].Candidate.Name != "first" {
		t.Fatal("Rank mutated its input")
	}
}

func TestShippedCandidatesAreSane(t *testing.T) {
	direct := DirectCandidates()
	if len(direct) == 0 {
		t.Fatal("no direct candidates shipped")
	}
	// Plain resolvers must never lead the shipped list: the order breaks ties, and
	// leading with a forgeable resolver would bias the pick toward it.
	if direct[0].Kind == KindPlain {
		t.Errorf("direct list leads with a plain resolver: %s", direct[0].Name)
	}
	var encrypted int
	for _, c := range direct {
		if c.Kind.Encrypted() {
			encrypted++
		}
		if c.Address == "" || c.Name == "" {
			t.Errorf("incomplete candidate: %+v", c)
		}
	}
	if encrypted == 0 {
		t.Error("direct list has no encrypted option at all")
	}
	for _, c := range RemoteCandidates() {
		if c.Kind == KindPlain {
			t.Errorf("remote list carries a plain resolver (%s); tunnelled DNS should always be encrypted", c.Name)
		}
	}
}
