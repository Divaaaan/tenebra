//go:build !windows

package main

import (
	"errors"
	"github.com/Divaaaan/tenebra/core/control"
)

func configureHostProtection(d *control.Daemon) { d.UseLegacyEngineProtection() }
func releaseNativeHostProtection() error {
	return errors.New("persistent host protection cleanup is Windows-only")
}
