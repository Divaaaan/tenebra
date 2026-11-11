package maintenancehost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/Divaaaan/tenebra/internal/securefs"
	"path/filepath"
	"sync"
)

const maxConfigBytes = 64 << 20

// ConfigLease holds a separate immutable stage until the supervised process has
// actually exited. Mutable engine caches and logs must live outside this tree.
type ConfigLease struct {
	mu     sync.Mutex
	stage  *securefs.Stage
	lease  *securefs.PathLease
	closed bool
}

func StageConfig(ctx context.Context, root *securefs.Root, data []byte) (result *ConfigLease, err error) {
	if ctx == nil || root == nil {
		return nil, securefs.ErrUntrusted
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if len(data) == 0 || len(data) > maxConfigBytes || !json.Valid(data) {
		return nil, securefs.ErrLimit
	}
	owned := bytes.Clone(data)
	stage, err := root.NewStage("engine-config")
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, stage.Close())
		}
	}()
	state, err := stage.Root().ReplaceFile("config.json", owned)
	if err != nil {
		return nil, err
	}
	if state != securefs.Committed {
		return nil, securefs.ErrChanged
	}
	lease, err := stage.Root().LeaseTree(ctx, securefs.Limits{MaxFiles: 1, MaxBytes: int64(len(owned))}, func(_ context.Context, lease *securefs.PathLease) error {
		files := lease.Files()
		if len(files) != 1 || files[0] != "config.json" {
			return securefs.ErrChanged
		}
		copied, err := lease.ReadFile("config.json", int64(len(owned)))
		if err != nil {
			return err
		}
		if !bytes.Equal(copied, owned) {
			return securefs.ErrChanged
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &ConfigLease{stage: stage, lease: lease}, nil
}

func (c *ConfigLease) Path() (string, error) {
	if c == nil {
		return "", securefs.ErrClosed
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return "", securefs.ErrClosed
	}
	path, err := c.lease.Path()
	if err != nil {
		return "", err
	}
	return filepath.Join(path, "config.json"), nil
}
func (c *ConfigLease) Close() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	if err := c.lease.Close(); err != nil {
		return err
	}
	if err := c.stage.Close(); err != nil {
		return err
	}
	c.closed = true
	return nil
}
