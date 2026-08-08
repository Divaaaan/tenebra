// Package dpi holds the client-side DPI-bypass engine's domain rules: the
// bypass strategies the client ships, the rules that turn one into a command
// line, and where the engine's binary and its loopback port come from.
//
// The engine is ByeDPI (`ciadpi`): an ordinary userspace process that listens as
// a SOCKS5 proxy on loopback and reshapes the opening packets of every
// connection it forwards, so a filter that keys on the plaintext handshake sees
// something it cannot match. It needs no driver and no elevation, which is what
// lets the bypass be a property of the direct leg of routing rather than a
// second, privileged tunnel mode. What it is not: it changes no IP and encrypts
// nothing, so it is a way past a block, never a way to be anonymous.
//
// Running and supervising the process is adapters/byedpi's job; this package
// stays free of process handling so the config builder can depend on it. That
// dependency direction is why nothing here imports core/singbox: the builder
// imports this package to name the bypass binary in the route rule that keeps
// the bypass process's own traffic out of the tunnel, and an import cannot run
// both ways.
package dpi

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
)

const (
	// BinaryEnv overrides binary resolution when set, so an operator (or a test,
	// or the desktop shell handing down a bundled resource path) can point at a
	// specific ByeDPI build. It mirrors TENEBRA_SINGBOX for sing-box.
	BinaryEnv = "TENEBRA_BYEDPI"

	// LoopbackHost is the only address the bypass listener is ever bound to. The
	// proxy forwards anything that reaches it, with no authentication of any
	// kind, so a listener reachable from the network would be an open proxy on
	// someone else's behalf.
	LoopbackHost = "127.0.0.1"

	// DefaultPort is the loopback port the bypass listener falls back to when no
	// free one can be picked. It is the same number as singbox.DefaultDPIPort,
	// which is what the generated config points the "dpi" outbound at; the
	// constant is repeated rather than imported because the import runs the other
	// way (see the package comment). The two must stay in sync.
	DefaultPort = 2081
)

// A Runner owns one ByeDPI process.
//
// It is deliberately narrower than control.Runner: that interface also exposes
// Stats and Probe, both of which read sing-box's clash API, and ByeDPI has no
// control surface at all — it is a plain SOCKS proxy with a command line. Its
// health is answered by dialling the listener instead, which the implementation
// offers beyond this interface.
type Runner interface {
	// Start spawns the engine with args (as rendered by BuildArgs) and returns
	// once the process exists; a later failure is reported through Done.
	Start(ctx context.Context, args []string) error
	// Stop terminates the process and waits for it to exit. It is idempotent.
	Stop() error
	// Done delivers the process's exit: the exit error once (nil on a clean
	// exit), after which the channel is closed. Before any Start it blocks.
	Done() <-chan error
	// Logs returns a copy of the most recent process output lines, newest last —
	// the in-memory tail kept for diagnostics, so a bypass failure is visible in
	// the UI log rather than swallowed.
	Logs() []string
}

// BinaryName is the expected ByeDPI filename for the current platform. The
// config builder also names it in the route rule that sends the bypass
// process's own connections direct — without that rule the proxy would route
// through the tunnel it is feeding, and the two would loop.
func BinaryName() string {
	if runtime.GOOS == "windows" {
		return "ciadpi.exe"
	}
	return "ciadpi"
}

// ResolveBinary returns the path to the ByeDPI binary: the BinaryEnv override
// when set, otherwise the platform-named binary sitting next to the running
// executable, which is where both the installer and the dev build put it.
func ResolveBinary() (string, error) {
	if p := os.Getenv(BinaryEnv); p != "" {
		return p, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("dpi: locate executable: %w", err)
	}
	return filepath.Join(filepath.Dir(exe), BinaryName()), nil
}

// FreePort picks a free TCP port on loopback for the bypass listener, falling
// back to DefaultPort when one cannot be picked at all.
//
// The port is released before the engine binds it, so this is a hint rather
// than a reservation: another process can take it in between. That window is
// why the runner's readiness check speaks the SOCKS greeting instead of merely
// dialling — a squatted port answers a dial but not a handshake, and the caller
// finds out before traffic is routed into it.
func FreePort() int {
	l, err := net.Listen("tcp", net.JoinHostPort(LoopbackHost, "0"))
	if err != nil {
		return DefaultPort
	}
	defer l.Close()

	addr, ok := l.Addr().(*net.TCPAddr)
	if !ok {
		return DefaultPort
	}
	return addr.Port
}
