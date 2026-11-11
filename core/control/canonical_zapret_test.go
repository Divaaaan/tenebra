package control

import (
	"context"
	"reflect"
	"testing"

	"github.com/Divaaaan/tenebra/core/state"
)

func TestCanonicalBypassStrategyCommitsBeforeInertLauncher(t *testing.T) {
	d, store, data := newCanonicalTestDaemon(t)
	const strategy = "general (SYNTHETIC ONLY)"
	seedBypassBundle(t, store.Profiles().Dir(), strategy)
	started := stubStarts(d)
	before := store.View()
	unblock := blockCanonicalFile(t, data)
	response := d.handleStartZapret(context.Background(), Request{ID: 1, Cmd: CmdStartZapret, Name: strategy})
	if response.Ok || response.Error != "persistence_error" || len(started.names()) != 0 || !reflect.DeepEqual(before, store.View()) {
		t.Fatal("bypass launched/projected before confirmed preference", response)
	}
	unblock()
	d.newStartRunner = func(_ string, _ bool) startRunner {
		prefs := store.View().Preferences
		if prefs.ZapretEnabled == nil || !*prefs.ZapretEnabled || prefs.ZapretStrategy != strategy {
			t.Fatal("launcher reached before durable selected strategy and on/off answer")
		}
		return started
	}
	response = d.handleStartZapret(context.Background(), Request{ID: 2, Cmd: CmdStartZapret, Name: strategy})
	if !response.Ok || len(started.names()) != 1 {
		t.Fatal(response)
	}
	if _, err := d.CommitCanonical(context.Background(), state.AnyRevision, func(s *state.Snapshot) error { s.Preferences.AdBlock = true; return nil }); err != nil {
		t.Fatal(err)
	}
	next := "another measured strategy"
	if err := d.commitZapretPreference(context.Background(), nil, &next); err != nil {
		t.Fatal(err)
	}
	prefs := store.View().Preferences
	if !prefs.AdBlock || prefs.ZapretEnabled == nil || !*prefs.ZapretEnabled || prefs.ZapretStrategy != next {
		t.Fatal("automatic strategy mutation overwrote unrelated preference or user's wish")
	}
	unblock = blockCanonicalFile(t, data)
	defer unblock()
	if err := d.recordZapretWish(false); err == nil {
		t.Fatal("failed OFF persistence was hidden")
	}
	prefs = store.View().Preferences
	if prefs.ZapretEnabled == nil || !*prefs.ZapretEnabled {
		t.Fatal("unsaved OFF projected")
	}
	if response := d.handleStopZapret(context.Background(), Request{ID: 3, Cmd: CmdStopZapret}); response.Ok || response.Error != "persistence_error" {
		t.Fatal("stop bypassed preference commit gate", response)
	}
}
