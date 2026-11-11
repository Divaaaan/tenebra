package sessionauth

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"

	"github.com/Divaaaan/tenebra/internal/securefs"
)

type FileOperatorPolicy struct{}

func (FileOperatorPolicy) Load(ctx context.Context) ([]uint32, error) {
	return loadPolicyFile(ctx, "/etc/tenebra/operators.json")
}

func loadPolicyFile(ctx context.Context, path string) (uids []uint32, failure error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(path) || filepath.Base(path) != "operators.json" {
		return nil, ErrOperatorPolicy
	}
	root, err := securefs.OpenRoot(filepath.Dir(path), securefs.Policy{Mode: securefs.MachineContainer}, false)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, errors.Join(ErrOperatorPolicy, err)
	}
	defer func() { failure = errors.Join(failure, root.Close()) }()
	body, err := root.ReadMachinePrivateMetadata(ctx, "operators.json")
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, errors.Join(ErrOperatorPolicy, err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return decodeOperatorPolicy(body)
}
