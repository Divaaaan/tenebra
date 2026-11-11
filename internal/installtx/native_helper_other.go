//go:build !linux && !darwin

package installtx

import (
	"context"
	"github.com/Divaaaan/tenebra/internal/securefs"
)

func RunNativeHelperCommand(context.Context, HelperCommand) (Result, error) {
	return Result{}, securefs.ErrUnsupported
}
