//go:build linux

package control

import (
	"context"
	"errors"
	"testing"
)

type linuxConsoleFixture func(context.Context) (*uint32, error)

func (f linuxConsoleFixture) ActivePhysicalUID(ctx context.Context) (*uint32, error) { return f(ctx) }

type linuxOperatorsFixture func(context.Context) ([]uint32, error)

func (f linuxOperatorsFixture) Load(ctx context.Context) ([]uint32, error) { return f(ctx) }

func TestRuntimeOwnerNotConsole(t *testing.T) {
	noPhysicalSession := linuxConsoleFixture(func(context.Context) (*uint32, error) { return nil, nil })
	noOperators := linuxOperatorsFixture(func(context.Context) ([]uint32, error) { return nil, nil })
	if linuxPeerAllowed(1001, 0, noPhysicalSession, noOperators, func(string) {}) {
		t.Fatal("a runtime/session owner was admitted without an active physical seat or operator policy")
	}
}

func TestLinuxConsoleAndOperatorPeersHaveBoundedAdmission(t *testing.T) {
	uid := uint32(1000)
	console := linuxConsoleFixture(func(ctx context.Context) (*uint32, error) {
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("physical lookup has no deadline")
		}
		return &uid, nil
	})
	operators := linuxOperatorsFixture(func(context.Context) ([]uint32, error) { return []uint32{1001}, nil })
	for _, tt := range []struct {
		peer int
		want bool
	}{{1000, true}, {1001, true}, {1002, false}} {
		if got := linuxPeerAllowed(tt.peer, 0, console, operators, func(string) {}); got != tt.want {
			t.Fatal(tt.peer, got, tt.want)
		}
	}
	unavailable := linuxConsoleFixture(func(context.Context) (*uint32, error) { return nil, errors.New("no physical seat") })
	if !linuxPeerAllowed(1001, 0, unavailable, operators, func(string) {}) || linuxPeerAllowed(1000, 0, unavailable, operators, func(string) {}) {
		t.Fatal("physical lookup failure changed trusted operator admission")
	}
}

func TestLinuxRootAndSelfSkipConsoleAndOperatorLookups(t *testing.T) {
	console := linuxConsoleFixture(func(context.Context) (*uint32, error) { t.Fatal("root/self queried console"); return nil, nil })
	operators := linuxOperatorsFixture(func(context.Context) ([]uint32, error) { t.Fatal("root/self queried policy"); return nil, nil })
	for _, tt := range []struct{ peer, self int }{{0, 1000}, {1000, 1000}, {0, 0}} {
		if !linuxPeerAllowed(tt.peer, tt.self, console, operators, func(string) {}) {
			t.Fatal(tt)
		}
	}
}

func TestLinuxInvalidOperatorPolicyRefusesOtherPeers(t *testing.T) {
	console := linuxConsoleFixture(func(context.Context) (*uint32, error) { t.Fatal("invalid policy queried console"); return nil, nil })
	operators := linuxOperatorsFixture(func(context.Context) ([]uint32, error) { return nil, errors.New("untrusted operator file") })
	warned := false
	if linuxPeerAllowed(1000, 0, console, operators, func(string) { warned = true }) || !warned {
		t.Fatal("untrusted operator policy was ignored")
	}
}

func TestDefaultSocketPathIsRunTenebra(t *testing.T) {
	if DefaultSocketPath != "/run/tenebra.sock" {
		t.Fatal(DefaultSocketPath)
	}
}
