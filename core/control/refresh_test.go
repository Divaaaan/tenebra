package control

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/tenebra-vpn/tenebra/core/model"
	"github.com/tenebra-vpn/tenebra/core/profile"
	"github.com/tenebra-vpn/tenebra/core/subscription"
)

// fakeFetch is an injectable subscription fetcher keyed by URL. A URL with no
// entry returns an error, standing in for a network failure.
type fakeFetch struct {
	bodies  map[string]string
	headers map[string]http.Header
	calls   []string
}

func (f *fakeFetch) fetch(_ context.Context, url string) ([]byte, http.Header, error) {
	f.calls = append(f.calls, url)
	body, ok := f.bodies[url]
	if !ok {
		return nil, nil, errors.New("fakeFetch: no body for " + url)
	}
	h := f.headers[url]
	if h == nil {
		h = http.Header{}
	}
	return []byte(body), h, nil
}

// userInfoHeader builds a Subscription-Userinfo header value.
func userInfoHeader(upload, download, total int64, expire time.Time) http.Header {
	v := "upload=" + strconv.FormatInt(upload, 10) +
		"; download=" + strconv.FormatInt(download, 10) +
		"; total=" + strconv.FormatInt(total, 10)
	if !expire.IsZero() {
		v += "; expire=" + strconv.FormatInt(expire.Unix(), 10)
	}
	h := http.Header{}
	h.Set("Subscription-Userinfo", v)
	return h
}

// daemonWithFetch builds a daemon over a fresh temp store with an injected
// fetcher, returning both so a test can seed the store and assert on it.
func daemonWithFetch(t *testing.T, f *fakeFetch) (*Daemon, *profile.Store) {
	t.Helper()
	store, err := profile.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	d := NewDaemon(store, newFakeRunner())
	d.fetch = f.fetch
	return d, store
}

// vlessLink is a syntactically valid VLESS link the parser accepts offline.
const vlessLink = "vless://11111111-2222-3333-4444-555555555555@a.example.com:443?security=reality&pbk=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA&sni=example.com#node-a"

func mustParse(t *testing.T, link string) model.Node {
	t.Helper()
	n, err := subscription.ParseLink(link)
	if err != nil {
		t.Fatalf("parse link: %v", err)
	}
	return n
}

func seedSubscription(t *testing.T, store *profile.Store, name, url string) profile.Profile {
	t.Helper()
	p, err := profile.NewProfile(name, profile.SourceSubscription, url, []model.Node{mustParse(t, vlessLink)})
	if err != nil {
		t.Fatalf("seed subscription: %v", err)
	}
	if err := store.Add(p); err != nil {
		t.Fatalf("add subscription: %v", err)
	}
	return p
}

func seedManual(t *testing.T, store *profile.Store, name string) profile.Profile {
	t.Helper()
	p, err := profile.NewProfile(name, profile.SourceManual, "", []model.Node{mustParse(t, vlessLink)})
	if err != nil {
		t.Fatalf("seed manual: %v", err)
	}
	if err := store.Add(p); err != nil {
		t.Fatalf("add manual: %v", err)
	}
	return p
}

func mustGet(t *testing.T, store *profile.Store, id string) profile.Profile {
	t.Helper()
	p, ok := store.Get(id)
	if !ok {
		t.Fatalf("profile %s not found", id)
	}
	return p
}

// TestRefreshAllSubscriptionsSelectsOnlySubscriptions checks the sweep fetches
// only subscription profiles with a URL, leaving manual and URL-less profiles
// untouched, and maps the user-info header onto the refreshed profile.
func TestRefreshAllSubscriptionsSelectsOnlySubscriptions(t *testing.T) {
	const subURL = "https://example.invalid/sub"
	f := &fakeFetch{
		bodies: map[string]string{subURL: vlessLink},
		headers: map[string]http.Header{
			subURL: userInfoHeader(1<<30, 2<<30, 100<<30, time.Unix(1893456000, 0)),
		},
	}
	d, store := daemonWithFetch(t, f)

	sub := seedSubscription(t, store, "Provider", subURL)
	seedManual(t, store, "Hand-added")
	// A subscription with no URL must be skipped (it can't be fetched).
	noURL, err := profile.NewProfile("URL-less", profile.SourceSubscription, "", []model.Node{mustParse(t, vlessLink)})
	if err != nil {
		t.Fatalf("build url-less: %v", err)
	}
	if err := store.Add(noURL); err != nil {
		t.Fatalf("add url-less: %v", err)
	}

	changed := d.refreshAllSubscriptions(context.Background())
	if !changed {
		t.Fatal("expected refreshAllSubscriptions to report a change")
	}

	// Only the one subscription with a URL should have been fetched.
	if len(f.calls) != 1 || f.calls[0] != subURL {
		t.Fatalf("expected a single fetch of %q, got %v", subURL, f.calls)
	}

	got := mustGet(t, store, sub.ID)
	if want := int64((1 << 30) + (2 << 30)); got.TrafficUsed != want {
		t.Errorf("TrafficUsed = %d, want %d", got.TrafficUsed, want)
	}
	if got.TrafficTotal != 100<<30 {
		t.Errorf("TrafficTotal = %d, want %d", got.TrafficTotal, int64(100<<30))
	}
	if got.ExpiresAt == nil || got.ExpiresAt.Unix() != 1893456000 {
		t.Errorf("ExpiresAt = %v, want unix 1893456000", got.ExpiresAt)
	}
}

// TestRefreshAllSubscriptionsNoSubscriptions checks the sweep is a clean no-op
// with no subscription profiles: no fetches, no change.
func TestRefreshAllSubscriptionsNoSubscriptions(t *testing.T) {
	f := &fakeFetch{bodies: map[string]string{}}
	d, store := daemonWithFetch(t, f)
	seedManual(t, store, "Hand-added")

	if d.refreshAllSubscriptions(context.Background()) {
		t.Error("expected no change with only manual profiles")
	}
	if len(f.calls) != 0 {
		t.Errorf("expected no fetches, got %v", f.calls)
	}
}

// TestRefreshAllSubscriptionsFailureSkips checks that a fetch failure on one
// profile neither aborts the sweep nor drops that profile's existing data: the
// healthy profile is still refreshed, and the failing one keeps its prior usage.
func TestRefreshAllSubscriptionsFailureSkips(t *testing.T) {
	const goodURL = "https://example.invalid/good"
	const badURL = "https://example.invalid/bad" // no body => fetch error
	f := &fakeFetch{
		bodies:  map[string]string{goodURL: vlessLink},
		headers: map[string]http.Header{goodURL: userInfoHeader(0, 5<<30, 50<<30, time.Time{})},
	}
	d, store := daemonWithFetch(t, f)

	good := seedSubscription(t, store, "Good", goodURL)
	bad := seedSubscription(t, store, "Bad", badURL)
	// Give the failing profile prior usage we expect to survive the failed sweep.
	bad.TrafficUsed = 9 << 30
	bad.TrafficTotal = 80 << 30
	if err := store.Update(bad); err != nil {
		t.Fatalf("seed bad usage: %v", err)
	}

	changed := d.refreshAllSubscriptions(context.Background())
	if !changed {
		t.Fatal("expected the good profile to register a change despite the bad one failing")
	}

	gotGood := mustGet(t, store, good.ID)
	if gotGood.TrafficTotal != 50<<30 {
		t.Errorf("good TrafficTotal = %d, want %d", gotGood.TrafficTotal, int64(50<<30))
	}

	gotBad := mustGet(t, store, bad.ID)
	if gotBad.TrafficUsed != 9<<30 || gotBad.TrafficTotal != 80<<30 {
		t.Errorf("bad profile data was disturbed: used=%d total=%d, want 9GiB/80GiB",
			gotBad.TrafficUsed, gotBad.TrafficTotal)
	}
}

// TestManualRefreshEmitsProfilesEvent drives the full server and asserts the
// manual refresh_subscription command emits a profiles event after it changes a
// stored profile, proving the event reaches the wire.
func TestManualRefreshEmitsProfilesEvent(t *testing.T) {
	h := newHarness(t)

	const subURL = "https://example.invalid/sub"
	f := &fakeFetch{
		bodies: map[string]string{subURL: vlessLink},
		headers: map[string]http.Header{
			subURL: userInfoHeader(1<<30, 2<<30, 100<<30, time.Unix(1893456000, 0)),
		},
	}
	h.daemon.fetch = f.fetch

	// Import a subscription so there is something to refresh.
	h.send(Request{ID: 1, Cmd: CmdImportSubscription, URL: subURL, Name: "Provider"})
	imp := h.await()
	var impOut struct {
		Profile profile.Profile `json:"profile"`
	}
	h.dataInto(imp, &impOut)

	// Change the upstream usage so the refresh registers a material change.
	f.headers[subURL] = userInfoHeader(2<<30, 4<<30, 100<<30, time.Unix(1893456000, 0))

	h.send(Request{ID: 2, Cmd: CmdRefreshSub, Profile: impOut.Profile.ID})
	ev := h.awaitEvent(EventProfiles)
	if ev["event"] != EventProfiles {
		t.Fatalf("expected a profiles event, got %v", ev)
	}
	// The profiles event is signal-only: it carries the name and nothing else.
	if len(ev) != 1 {
		t.Errorf("profiles event should carry no payload, got %v", ev)
	}

	r := h.await()
	if !r.Ok {
		t.Fatalf("refresh failed: %s", r.Error)
	}
}

// TestRefreshProfileUnchanged checks that re-fetching an identical subscription
// (same nodes, same usage) reports no change, so the UI is not churned.
func TestRefreshProfileUnchanged(t *testing.T) {
	const subURL = "https://example.invalid/stable"
	hdr := userInfoHeader(1<<30, 1<<30, 10<<30, time.Unix(1893456000, 0))
	f := &fakeFetch{
		bodies:  map[string]string{subURL: vlessLink},
		headers: map[string]http.Header{subURL: hdr},
	}
	d, store := daemonWithFetch(t, f)
	p := seedSubscription(t, store, "Stable", subURL)

	// First refresh establishes the usage/nodes.
	if _, _, err := d.refreshProfile(context.Background(), mustGet(t, store, p.ID)); err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	// Second refresh with the same body+header must report no material change.
	_, changed, err := d.refreshProfile(context.Background(), mustGet(t, store, p.ID))
	if err != nil {
		t.Fatalf("second refresh: %v", err)
	}
	if changed {
		t.Error("expected no change refreshing an identical subscription")
	}
}
