package windows

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

// The live exit switch happens entirely over the loopback clash API: the tunnel
// is steered by pointing its selector at another outbound, and a candidate exit
// is measured by asking the same API to fetch a chosen destination through that
// outbound. These cover the request shapes and the auth both depend on, without
// a sing-box.

// TestSelectPutsTheSelector: the switch is a PUT to /proxies/<group> carrying
// {"name": <member>}, authenticated with the run's secret. Anything else and the
// running tunnel simply does not move.
func TestSelectPutsTheSelector(t *testing.T) {
	var gotMethod, gotPath, gotAuth, gotType string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		gotMethod, gotPath = req.Method, req.URL.Path
		gotAuth = req.Header.Get("Authorization")
		gotType = req.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(req.Body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	r := New()
	r.ClashPort = clashPortFromURL(t, srv.URL)
	r.clashSecret = "tok"

	if err := r.Select(context.Background(), "proxy", "Amsterdam-2"); err != nil {
		t.Fatalf("Select: %v", err)
	}
	if gotMethod != http.MethodPut {
		t.Errorf("method = %s, want PUT", gotMethod)
	}
	if gotPath != "/proxies/proxy" {
		t.Errorf("path = %q, want /proxies/proxy", gotPath)
	}
	if gotAuth != "Bearer tok" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer tok")
	}
	if !strings.HasPrefix(gotType, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", gotType)
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(gotBody, &body); err != nil {
		t.Fatalf("body %q is not JSON: %v", gotBody, err)
	}
	if body.Name != "Amsterdam-2" {
		t.Errorf("body name = %q, want Amsterdam-2", body.Name)
	}
}

// TestSelectReportsRefusal: sing-box answers 400 for a member it does not have.
// That has to surface as an error, because the caller's whole fallback — put the
// old exit back, reconnect instead — hangs on knowing the switch did not take.
func TestSelectReportsRefusal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"message":"Selector update error: not found"}`))
	}))
	defer srv.Close()

	r := New()
	r.ClashPort = clashPortFromURL(t, srv.URL)

	err := r.Select(context.Background(), "proxy", "gone")
	if err == nil {
		t.Fatal("Select: want an error for a refused selection, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %v, want it to carry the API's reason", err)
	}
}

// TestSelectURLEscapesGroup guards against a group name with reserved characters
// reshaping the request.
func TestSelectURLEscapesGroup(t *testing.T) {
	u, err := url.Parse(selectURL(9090, "weird name/with?reserved&chars"))
	if err != nil {
		t.Fatalf("selectURL produced an unparseable URL: %v", err)
	}
	if u.Host != "127.0.0.1:9090" {
		t.Errorf("host = %q, want 127.0.0.1:9090", u.Host)
	}
	if u.Path != "/proxies/weird name/with?reserved&chars" {
		t.Errorf("decoded path = %q", u.Path)
	}
	if u.Query().Get("reserved") != "" {
		t.Error("a reserved character in the group leaked into the query string")
	}
}

// TestProbeViaMeasuresTheNamedTargetThroughTheNamedOutbound: a candidate exit is
// judged on several destinations through its OWN outbound, so both have to reach
// the request — measuring the selector, or always the default target, would judge
// the wrong thing.
func TestProbeViaMeasuresTheNamedTargetThroughTheNamedOutbound(t *testing.T) {
	var gotPath, gotTarget string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		gotPath = req.URL.Path
		gotTarget = req.URL.Query().Get("url")
		_, _ = w.Write([]byte(`{"delay":77}`))
	}))
	defer srv.Close()

	r := New()
	r.ClashPort = clashPortFromURL(t, srv.URL)

	delay, err := r.ProbeVia(context.Background(), "Amsterdam-2", "https://example.invalid/generate_204")
	if err != nil {
		t.Fatalf("ProbeVia: %v", err)
	}
	if delay != 77 {
		t.Errorf("delay = %d, want 77", delay)
	}
	if gotPath != "/proxies/Amsterdam-2/delay" {
		t.Errorf("path = %q, want the candidate's own outbound", gotPath)
	}
	if gotTarget != "https://example.invalid/generate_204" {
		t.Errorf("url param = %q, want the requested target", gotTarget)
	}
}

// TestProbeKeepsTheDefaultTarget: Probe is ProbeVia against the built-in control
// URL, so the connect and health paths measure exactly what they always did.
func TestProbeKeepsTheDefaultTarget(t *testing.T) {
	var gotTarget string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		gotTarget = req.URL.Query().Get("url")
		_, _ = w.Write([]byte(`{"delay":5}`))
	}))
	defer srv.Close()

	r := New()
	r.ClashPort = clashPortFromURL(t, srv.URL)

	if _, err := r.Probe(context.Background(), "proxy"); err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if gotTarget != probeURL {
		t.Errorf("url param = %q, want %q", gotTarget, probeURL)
	}
	// Keeps the port helper honest alongside the rest.
	if !strings.Contains(delayURLFor(1234, "t", probeURL), strconv.Itoa(1234)) {
		t.Error("delayURLFor ignored the port")
	}
}
