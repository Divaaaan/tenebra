//go:build windows

package control

import (
	"fmt"
	"net"

	"github.com/Microsoft/go-winio"
)

// PipeName is the named pipe the control protocol is served on when the core
// runs detached from the UI — as the Windows service, or in the --pipe console
// mode. One well-known name: the UI discovers the core by dialling it.
const PipeName = `\\.\pipe\tenebra`

// pipeSecurityDescriptor is the DACL applied to the pipe. It admits exactly
// three identities, the same trust decision Tailscale's LocalAPI pipe makes:
//
//	SY (LocalSystem)      - the service itself;
//	BA (Administrators)   - elevated processes;
//	IU (INTERACTIVE)      - any locally logged-in user, which is what lets the
//	                        unprivileged GUI drive the privileged service.
//
// The interactive ACE grants only read/write data, read attributes, read control
// and synchronize (0x120083). GENERIC_WRITE also includes the 0x4 server-instance
// creation bit, so it would let an interactive client create a competing server.
// Clients must request this exact access mask rather than GENERIC_READ/WRITE.
// Network logons never carry INTERACTIVE; authenticated peer checks still run
// after accept. See docs/control-protocol.md for the complete trust model.
const pipeClientAccess uint32 = 0x120083
const pipeSecurityDescriptor = "D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;0x120083;;;IU)"

// ListenPipe opens the named pipe listener the control protocol is served on.
// name is PipeName in production; tests pass unique names so parallel runs
// don't collide. The returned listener plugs into ServeListener.
func ListenPipe(name string) (net.Listener, error) {
	l, err := winio.ListenPipe(name, &winio.PipeConfig{
		SecurityDescriptor: pipeSecurityDescriptor,
	})
	if err != nil {
		return nil, fmt.Errorf("control: listen on %s: %w", name, err)
	}
	return l, nil
}
