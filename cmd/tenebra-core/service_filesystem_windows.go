//go:build windows

package main

import (
	"context"
	"errors"
	"io"

	"github.com/Divaaaan/tenebra/internal/securefs"
	"golang.org/x/sys/windows"
)

func openServiceFilesystem(ctx context.Context) (daemonFilesystem, error) {
	// A mutable process environment is never the service's machine anchor.
	path, err := windows.KnownFolderPath(windows.FOLDERID_ProgramData, windows.KF_FLAG_DEFAULT)
	if err != nil {
		return daemonFilesystem{}, err
	}
	container := securefs.Policy{Mode: securefs.MachineContainer}
	anchor, err := securefs.OpenRoot(path, container, false)
	if err != nil {
		return daemonFilesystem{}, err
	}
	filesystem, err := openMachineLayout(ctx, anchor, securefs.Policy{Mode: securefs.MachinePrivate}, container)
	if err != nil {
		return daemonFilesystem{}, errors.Join(err, anchor.Close())
	}
	filesystem.closers = append([]io.Closer{anchor}, filesystem.closers...)
	return filesystem, nil
}
