package main

import (
	"encoding/hex"
	"flag"
	"strings"

	"github.com/Divaaaan/tenebra/core/control"
	"github.com/Divaaaan/tenebra/internal/installtx"
	"github.com/Divaaaan/tenebra/internal/securefs"
)

var maintenanceTx = flag.String("maintenance-tx", "", "protected installer transaction (root Unix service only)")
var maintenanceLaunch = flag.String("launch-id", "", "identified installer process launch (root Unix service only)")

type runAuthority struct {
	managed             bool
	transaction, launch string
}
type managedBoot struct {
	startup                              func(*control.Daemon, bool)
	authority                            runAuthority
	paths                                installtx.PlatformPaths
	journal                              *installtx.JournalFile
	runtime                              installtx.RuntimeRef
	manifest                             installtx.Manifest
	runtimeRoot, readyRoot, socketOwners *securefs.Root
}

func opaqueLaunch(value, prefix string) bool {
	if !strings.HasPrefix(value, prefix) || len(value) != len(prefix)+32 {
		return false
	}
	value = strings.TrimPrefix(value, prefix)
	if value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
func selectRunAuthority(socket, unixRoot bool, tx, launch string) (runAuthority, error) {
	requested := tx != "" || launch != ""
	if unixRoot && socket {
		if !opaqueLaunch(tx, "it1_") || !opaqueLaunch(launch, "") {
			return runAuthority{}, installtx.ErrRecoveryRequired
		}
		return runAuthority{managed: true, transaction: tx, launch: launch}, nil
	}
	if requested {
		return runAuthority{}, installtx.ErrProtocol
	}
	return runAuthority{}, nil
}
