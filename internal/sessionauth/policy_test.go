package sessionauth

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
)

type fixturePolicy struct {
	uids []uint32
	err  error
}

func (p fixturePolicy) Load(context.Context) ([]uint32, error) { return p.uids, p.err }

type fixtureConsole func(context.Context) (*uint32, error)

func (source fixtureConsole) ActivePhysicalUID(ctx context.Context) (*uint32, error) {
	return source(ctx)
}

func TestOperatorPolicyStrictSchema(t *testing.T) {
	for _, body := range []string{
		`{}`, `{"schema":1}`, `{"schema":1,"uids":null}`, `{"schema":2,"uids":[]}`,
		`{"schema":1,"uids":[-1]}`, `{"schema":1,"uids":[4294967295]}`,
		`{"schema":1,"uids":[4294967296]}`, `{"schema":1,"uids":[1000.1]}`,
		`{"schema":1,"uids":[1000,1000]}`, `{"schema":1,"uids":["1000"]}`,
		`{"schema":1,"uids":[],"extra":true}`, `{"schema":1,"schema":1,"uids":[]}`,
		`{"schema":1,"uids":[],"uids":[1000]}`, `{"schema":1,"uids":[]} {}`,
	} {
		if _, err := decodeOperatorPolicy([]byte(body)); !errors.Is(err, ErrOperatorPolicy) {
			t.Errorf("accepted operator policy %s, %v", body, err)
		}
	}
	uids, err := decodeOperatorPolicy([]byte(`{"schema":1,"uids":[1001,0,1000]}`))
	if err != nil || !slices.Equal(uids, []uint32{0, 1000, 1001}) {
		t.Fatal(uids, err)
	}
	if uids, err := decodeOperatorPolicy([]byte(`{"schema":1,"uids":[]}`)); err != nil || len(uids) != 0 {
		t.Fatal(uids, err)
	}
}

func TestPhysicalConsoleAndOperatorsHaveIndependentAdmission(t *testing.T) {
	physical := uint32(1000)
	console := fixtureConsole(func(ctx context.Context) (*uint32, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > time.Second {
			t.Fatal("physical console query has no one-second bound")
		}
		return &physical, nil
	})
	operators := []uint32{1001}
	set, err := ResolveLinux(context.Background(), console, fixturePolicy{uids: operators})
	if err != nil || !set.Allows(1000) || !set.Allows(1001) || set.Allows(1002) {
		t.Fatal(set, err)
	}
	physical, operators[0] = 1002, 1002
	if !set.Allows(1000) || !set.Allows(1001) || set.Allows(1002) {
		t.Fatal("caller mutation changed the resolved admission set", set)
	}
}

func TestUnknownPhysicalConsoleRequiresTrustedOperator(t *testing.T) {
	unknown := fixtureConsole(func(context.Context) (*uint32, error) { return nil, ErrPhysicalConsole })
	if _, err := ResolveLinux(context.Background(), unknown, fixturePolicy{}); !errors.Is(err, ErrPhysicalConsole) {
		t.Fatal("unknown physical console was admitted", err)
	}
	set, err := ResolveLinux(context.Background(), unknown, fixturePolicy{uids: []uint32{1001}})
	if err != nil || !set.Allows(1001) || set.Allows(1000) || set.ConsoleUID != nil {
		t.Fatal(set, err)
	}
	called := false
	console := fixtureConsole(func(context.Context) (*uint32, error) { called = true; return nil, nil })
	if _, err := ResolveLinux(context.Background(), console, fixturePolicy{err: ErrOperatorPolicy}); !errors.Is(err, ErrOperatorPolicy) || called {
		t.Fatal("invalid operator policy was ignored", err, called)
	}
}

func TestPhysicalConsoleQueryRespectsCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	console := fixtureConsole(func(ctx context.Context) (*uint32, error) { <-ctx.Done(); return nil, ctx.Err() })
	if _, err := ResolveLinux(ctx, console, fixturePolicy{uids: []uint32{1000}}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("canceled admission succeeded", err)
	}
}
