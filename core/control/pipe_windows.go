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
// GRGW (generic read/write) is what winio and every stock pipe client request
// when dialling, so narrowing the client rights further would lock the GUI
// out. Network logons never carry the INTERACTIVE SID, so a remote caller
// needs administrator credentials to reach the pipe at all. See
// docs/control-protocol.md for the security model and its honest limits.
const pipeSecurityDescriptor = "D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;GRGW;;;IU)"

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
