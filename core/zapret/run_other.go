//go:build !windows

package zapret

import (
	"context"
	"errors"
	"path/filepath"
	"time"
)

// Runner is the non-Windows stub.
//
// zapret's Windows build is winws.exe on the WinDivert driver; there is no
// equivalent to drive here. The type exists so the control layer compiles and
// vets on every platform (the same split core/control uses for its own
// OS-specific pieces) and reports plainly that the feature is Windows-only
// rather than failing somewhere deeper with a confusing error.
type Runner struct {
	Dir          string
	Settle       time.Duration
	ProbeTimeout time.Duration
}

var errUnsupported = errors.New("zapret: поддерживается только на Windows")

func NewRunner(dir string) *Runner { return &Runner{Dir: dir} }

func (r *Runner) Start(context.Context, Strategy) (bool, error) { return false, errUnsupported }

func (r *Runner) Stop(context.Context) error { return nil }

func (r *Runner) Probe(context.Context, []string) []TargetResult { return nil }

func (r *Runner) Pick(context.Context, []Strategy, []string, func(Result)) ([]Result, int, error) {
	return nil, 0, errUnsupported
}

func (r *Runner) StrategyPath(name string) string {
	return filepath.Join(r.Dir, name+".bat")
}
