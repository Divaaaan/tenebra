package installtx

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type readyProbeFunc func(context.Context, ProcessIdentity, ReadyRequest) (ReadyReply, error)

func (f readyProbeFunc) Exchange(ctx context.Context, p ProcessIdentity, r ReadyRequest) (ReadyReply, error) {
	return f(ctx, p, r)
}
func readyFixture() ReadyExpectation {
	launch := strings.Repeat("b", 32)
	return ReadyExpectation{Schema: 1, StoreSchema: 2, TxID: "it1_" + strings.Repeat("a", 32), DaemonVersion: "0.6.0", LaunchID: launch,
		PreviousDaemonInstanceID: "previous_instance_identifier", CoreSHA256: strings.Repeat("c", 64), Transport: "unix", BusinessAdmission: "closed", StoreRevision: 41,
		Process: ProcessIdentity{PID: 12345, StartToken: "start-token-fixture", LaunchID: launch}}
}
func readyReply(e ReadyExpectation, r ReadyRequest) ReadyReply {
	return ReadyReply{Schema: e.Schema, StoreSchema: e.StoreSchema, Status: "ready", TxID: e.TxID, Challenge: r.Challenge, DaemonVersion: e.DaemonVersion,
		DaemonInstanceID: "new_instance_identifier", LaunchID: e.LaunchID, CoreSHA256: e.CoreSHA256, Transport: e.Transport, BusinessAdmission: "closed", StoreRevision: 41}
}
func TestReadinessRejectsForeignIdentity(t *testing.T) {
	e := readyFixture()
	for name, mutate := range map[string]func(*ReadyReply){
		"old-instance":          func(r *ReadyReply) { r.DaemonInstanceID = e.PreviousDaemonInstanceID },
		"wrong-launch":          func(r *ReadyReply) { r.LaunchID = strings.Repeat("d", 32) },
		"replayed-challenge":    func(r *ReadyReply) { r.Challenge = strings.Repeat("e", 32) },
		"digest":                func(r *ReadyReply) { r.CoreSHA256 = strings.Repeat("f", 64) },
		"version":               func(r *ReadyReply) { r.DaemonVersion = "0.5.9" },
		"transport":             func(r *ReadyReply) { r.Transport = "stdio" },
		"schema":                func(r *ReadyReply) { r.StoreSchema = 1 },
		"old-revision":          func(r *ReadyReply) { r.StoreRevision = 40 },
		"business-already-open": func(r *ReadyReply) { r.BusinessAdmission = "open" },
		"other-tx":              func(r *ReadyReply) { r.TxID = "it1_" + strings.Repeat("d", 32) },
	} {
		t.Run(name, func(t *testing.T) {
			probe := readyProbeFunc(func(ctx context.Context, p ProcessIdentity, r ReadyRequest) (ReadyReply, error) {
				if p != e.Process {
					t.Fatal("spawn identity not passed to peer verifier")
				}
				reply := readyReply(e, r)
				mutate(&reply)
				return reply, nil
			})
			if _, err := VerifyReady(context.Background(), probe, e); !errors.Is(err, ErrReadiness) {
				t.Fatalf("foreign readiness accepted: %v", err)
			}
		})
	}
	// Authenticated-peer failure is final even when an attacker could otherwise
	// manufacture a perfect reply. No business ServeListener is used here.
	probe := readyProbeFunc(func(context.Context, ProcessIdentity, ReadyRequest) (ReadyReply, error) {
		return ReadyReply{}, ErrReadiness
	})
	if _, err := VerifyReady(context.Background(), probe, e); !errors.Is(err, ErrReadiness) {
		t.Fatal(err)
	}
}
func TestReadinessKeepsLaterWritesAndBoundsWait(t *testing.T) {
	e := readyFixture()
	probe := readyProbeFunc(func(ctx context.Context, p ProcessIdentity, r ReadyRequest) (ReadyReply, error) {
		reply := readyReply(e, r)
		reply.StoreRevision = 99
		return reply, nil
	})
	if reply, err := VerifyReady(context.Background(), probe, e); err != nil || reply.StoreRevision != 99 {
		t.Fatal(reply, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	probe = readyProbeFunc(func(ctx context.Context, p ProcessIdentity, r ReadyRequest) (ReadyReply, error) {
		<-ctx.Done()
		return ReadyReply{}, ctx.Err()
	})
	if _, err := VerifyReady(ctx, probe, e); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("unbounded or accepted wait", err)
	}
}
