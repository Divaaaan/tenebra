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
// Every knob the control layer sets on a Runner has to exist here too, even
// where nothing reads it: core/control is platform-neutral and assigns them
// unconditionally, so a field present only in the Windows build breaks the vet
// and the build on every other platform. PinIfaceIndex was added to the Windows
// runner without a counterpart here and did exactly that.
type Runner struct {
	Dir               string
	Settle            time.Duration
	ProbeTimeout      time.Duration
	KeepVoiceInTunnel bool
	PinIfaceIndex     int
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
