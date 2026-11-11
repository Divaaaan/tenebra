//go:build !linux && !darwin

package installtx

import (
	"context"
	"github.com/Divaaaan/tenebra/internal/securefs"
)

func ReadPublicStatus() (Status, error)                 { return Status{}, securefs.ErrUnsupported }
func PublishPublicStatus(context.Context, Status) error { return securefs.ErrUnsupported }
