package control

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// TestDispatchRequestRecoversPanic drives the recover seam directly: a handler
// that panics on one request must yield an error response correlated by id
// rather than propagate, and the very next request must be dispatched normally —
// the loop invariant serveStream relies on.
func TestDispatchRequestRecoversPanic(t *testing.T) {
	calls := 0
	handle := func(_ context.Context, req Request) Response {
		calls++
		if req.Cmd == "boom" {
			panic("induced handler panic")
		}
		return newResult0(req.ID)
	}

	resp := dispatchRequest(context.Background(), Request{ID: 7, Cmd: "boom"}, handle)
	if resp.Ok {
		t.Fatal("a panicking handler must produce an error response")
	}
	if resp.ID != 7 {
		t.Errorf("recovered response id = %d, want 7 (the request's id)", resp.ID)
	}
	if !strings.Contains(resp.Error, "internal error") {
		t.Errorf("recovered error = %q, want it to mention an internal error", resp.Error)
	}

	// The dispatcher is reusable after a recovered panic: the next request runs.
	ok := dispatchRequest(context.Background(), Request{ID: 8, Cmd: "status"}, handle)
	if !ok.Ok || ok.ID != 8 {
		t.Errorf("request after a recovered panic = %+v, want ok id 8", ok)
	}
	if calls != 2 {
		t.Errorf("handle called %d times, want 2", calls)
	}
}

// TestServeSurvivesHandlerPanic proves the recover holds through the real Server
// and daemon: a panic inside a command handler surfaces as an error response for
// that request, and the session keeps serving — the daemon and its tunnel are
// not taken down. Without the recover the panic would abort the whole test
// binary (a panic on the serve goroutine has no other catcher).
func TestServeSurvivesHandlerPanic(t *testing.T) {
	h := newHarness(t)
	// Inject a panic on the fetch path so import_subscription panics inside
	// Handle (the seam is the codebase's established test hook; see the
	// autoconnect and DNS suites).
	h.daemon.fetch = func(context.Context, string) ([]byte, http.Header, error) {
		panic("induced fetch panic")
	}

	h.send(Request{ID: 1, Cmd: CmdImportSubscription, URL: "http://sub.invalid"})
	r := h.await()
	if r.Ok {
		t.Fatal("a panicking handler should surface as an error response, not success")
	}
	if r.ID != 1 {
		t.Errorf("error response id = %d, want 1", r.ID)
	}

	// The session survived: a following request is handled normally.
	h.send(Request{ID: 2, Cmd: CmdStatus})
	r2 := h.await()
	if !r2.Ok || r2.ID != 2 {
		t.Fatalf("session did not survive the recovered panic: %+v", r2)
	}
}
