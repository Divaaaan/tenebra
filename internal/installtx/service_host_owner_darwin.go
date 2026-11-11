//go:build darwin

package installtx

import (
	"context"
	"github.com/Divaaaan/tenebra/internal/securefs"
)

// The native launchd job/controller still needs its own verified ownership and
// whole-job stop implementation. Do not launch a production host outside that
// containment and later pretend a different OS job stopped an ambiguous fork.
func verifyServiceHostOwner(context.Context, *JournalFile) error { return securefs.ErrUnsupported }
