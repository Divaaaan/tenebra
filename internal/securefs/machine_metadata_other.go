//go:build !linux && !darwin

package securefs

import "context"

func (*Root) ReadMachinePrivateMetadata(context.Context, string) ([]byte, error) {
	return nil, ErrUnsupported
}

func (*Root) PublishMachineMetadata(context.Context, string, []byte, string, uint32) (CommitState, error) {
	return Unchanged, ErrUnsupported
}
