//go:build windows

package main

import (
	"errors"
	"log"
	"net"

	"github.com/Divaaaan/tenebra/core/control"
	"github.com/Divaaaan/tenebra/core/protection"
)

func releaseNativeHostProtection() error {
	b := protection.NewWindowsBackend(nil)
	if b == nil {
		return errors.New("host protection cleanup is unsupported on this Windows architecture")
	}
	return b.Remove() // does not require the engine, settings, or a running service
}

// Called only by real process entry points, before background jobs/autoconnect.
// Neither buildDaemon nor NewDaemon installs a resolver or invokes native WFP.
func configureHostProtection(d *control.Daemon) {
	d.SetProtection(protection.New(protection.NewWindowsBackend(d.EngineExecutablePath)))
	net.DefaultResolver = protection.NewResolver(d.ProtectionDNS)
	if err := d.RecoverProtectionAtStartup(); err != nil {
		log.Printf("tenebra-core: %v", err)
	}
}
