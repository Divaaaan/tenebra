package control

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/Divaaaan/tenebra/core/zapret"
)

func TestMaintenanceRejectsEveryBusinessEntry(t *testing.T) {
	d, store, _ := newCanonicalTestDaemon(t)
	var effects atomic.Int32
	d.SetZapretUpdater(
		func(context.Context) (zapret.Release, error) {
			effects.Add(1)
			return zapret.Release{}, errors.New("synthetic updater")
		},
		func(context.Context, string, zapret.Release) error {
			effects.Add(1)
			return errors.New("synthetic apply")
		},
		func(string) ([]zapret.Strategy, error) { effects.Add(1); return nil, errors.New("synthetic embedded") },
	)
	if err := store.Fence(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := store.View()
	ctx, cancel := context.WithCancel(withPeerPrivilege(context.Background(), true))
	cancel()
	for _, command := range []string{
		CmdImportSubscription, CmdImportLink, CmdImportLinks, CmdRemoveProfile, CmdRefreshSub,
		CmdConnect, CmdConnectV2, CmdSetMultihop, CmdSetMultihopV2, CmdSetSubscriptionHTTPPolicy,
		CmdStartSubscriptionOperation, CmdContinueSubscriptionOperation, CmdPing, CmdCheckNodes,
		CmdImportZapret, CmdListZapret, CmdPickZapret, CmdStartZapret, CmdStopZapret, CmdUpdateZapret,
		CmdSetZapretAutoUpdate, CmdSetRouting, CmdSetSplit, CmdSetPresets, CmdSetKillSwitch,
		CmdSetTLSFragment, CmdSetProxyMode, CmdSetTun, CmdSetAutoconnect, CmdSetAutoFailover,
		CmdSetDNS, CmdSetRules, CmdSetCrashReports, CmdCheckServices, CmdLeakCheck,
		CmdRunStunCheck, CmdRunSpeedTest, "future_business_command",
	} {
		t.Run(command, func(t *testing.T) {
			response := d.Handle(ctx, Request{ID: 1, Cmd: command})
			if response.Ok || response.Error != "maintenance_pending" {
				t.Errorf("business entry bypassed held admission: %s %#v", command, response)
			}
		})
	}
	if effects.Load() != 0 || !reflect.DeepEqual(before, store.View()) {
		t.Fatal("held commands changed canonical state or reached updater", effects.Load())
	}
	for _, command := range []string{CmdStatus, CmdListProfiles, CmdCollectDiagnostics, CmdDisconnect, CmdSubscriptionOperationStatus, CmdCancelSubscriptionOperation} {
		if response := d.Handle(ctx, Request{ID: 2, Cmd: command}); response.Error == "maintenance_pending" {
			t.Fatalf("safe control became unresponsive: %s", command)
		}
	}
}

func TestMaintenanceBackgroundStartupDoesNotInstallOrRunBundle(t *testing.T) {
	d, store, _ := newCanonicalTestDaemon(t)
	var effects atomic.Int32
	d.SetZapretUpdater(nil, nil, func(string) ([]zapret.Strategy, error) { effects.Add(1); return nil, errors.New("synthetic embedded") })
	if err := store.Fence(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	d.RunZapretAutoUpdate(ctx)
	d.refreshSubscriptionsTick(ctx)
	if d.AutoconnectOnStart() {
		t.Fatal("held startup began autoconnect")
	}
	if effects.Load() != 0 {
		t.Fatal("held startup reached bundle installer", effects.Load())
	}
}
