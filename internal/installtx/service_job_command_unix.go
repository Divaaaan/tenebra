//go:build linux || darwin

package installtx

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/Divaaaan/tenebra/internal/securefs"
)

type serviceCommandResult struct {
	Code   int
	Output []byte
}
type serviceOutput struct {
	body     []byte
	overflow bool
}

func (w *serviceOutput) Write(p []byte) (int, error) {
	remaining := MaxMetadataBytes - len(w.body)
	if len(p) > remaining {
		w.body = append(w.body, p[:remaining]...)
		w.overflow = true
		return remaining, securefs.ErrLimit
	}
	w.body = append(w.body, p...)
	return len(p), nil
}

// OS administration binaries are fixed trusted platform roles, never PATH or
// request-selected programs. Validate their no-follow root-owned bytes first;
// privileged OS/package updates are within the trusted administrator boundary.
func runServiceCommand(ctx context.Context, binary string, args ...string) (serviceCommandResult, error) {
	if binary != "/usr/bin/systemctl" && binary != "/bin/launchctl" {
		return serviceCommandResult{}, ErrProtocol
	}
	root, err := securefs.OpenRoot(filepath.Dir(binary), securefs.Policy{Mode: securefs.OperatorInput, Principal: "uid:0"}, false)
	if err != nil {
		return serviceCommandResult{}, errors.Join(ErrServiceManager, err)
	}
	_, readErr := root.ReadFile(filepath.Base(binary), 32<<20)
	if err := errors.Join(readErr, root.Close()); err != nil {
		return serviceCommandResult{}, errors.Join(ErrServiceManager, err)
	}
	bound, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(bound, binary, args...)
	cmd.Dir = "/"
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "HOME=/var/empty", "LANG=C", "LC_ALL=C", "SYSTEMD_COLORS=0"}
	cmd.WaitDelay = 2 * time.Second
	out := &serviceOutput{}
	cmd.Stdout, cmd.Stderr = out, io.Discard
	err = cmd.Run()
	if out.overflow {
		return serviceCommandResult{}, errors.Join(ErrServiceManager, securefs.ErrLimit)
	}
	if bound.Err() != nil {
		return serviceCommandResult{}, errors.Join(ErrServiceManager, bound.Err())
	}
	if err == nil {
		return serviceCommandResult{Output: out.body}, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() >= 0 {
		return serviceCommandResult{Code: exit.ExitCode(), Output: out.body}, nil
	}
	return serviceCommandResult{}, ErrServiceManager
}
