//go:build !windows && !linux && !darwin

package securefs

func newNativeOps() (nativeOps, error) { return nil, ErrUnsupported }
