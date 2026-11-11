//go:build !linux && !darwin && !windows

package installtx

import "github.com/Divaaaan/tenebra/internal/securefs"

func CurrentOperatorPrincipal() (string, error) { return "", securefs.ErrUnsupported }
