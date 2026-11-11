//go:build !linux && !darwin

package installtx

import "github.com/Divaaaan/tenebra/internal/securefs"

func NewReadyProbe(*securefs.Root, string) (ReadyProbe, error) { return nil, securefs.ErrUnsupported }
func ProcessStartToken(uint32) (string, error)                 { return "", securefs.ErrUnsupported }
