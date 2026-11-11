package main

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/Divaaaan/tenebra/core/control"
	"github.com/Divaaaan/tenebra/internal/securefs"
)

type bootstrapCloser struct{ calls int }

func (c *bootstrapCloser) Close() error { c.calls++; return nil }

func TestServiceBootstrapNeverBuildsAfterOpenFailureOrMissingRoots(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(map[bool]string{false: "open_failure", true: "missing_roots"}[missing], func(t *testing.T) {
			calls := 0
			closer := &bootstrapCloser{}
			bootstrap := daemonBootstrap{
				Open: func(context.Context) (daemonFilesystem, error) {
					if !missing {
						return daemonFilesystem{}, errRecoveryRequired
					}
					return daemonFilesystem{closers: []io.Closer{closer}}, nil
				},
				Build: func(*daemonFilesystem) (*control.Daemon, error) { calls++; return nil, nil },
			}
			_, _, err := bootstrap.Prepare(context.Background())
			if err == nil || calls != 0 || (missing && closer.calls != 1) {
				t.Fatalf("unsafe bootstrap reached consumers: calls=%d close=%d error=%v", calls, closer.calls, err)
			}
		})
	}
	if _, err := buildDaemonWithFilesystem(&daemonFilesystem{}); !errors.Is(err, securefs.ErrUntrusted) {
		t.Fatalf("nil roots selected legacy IO: %v", err)
	}
}
