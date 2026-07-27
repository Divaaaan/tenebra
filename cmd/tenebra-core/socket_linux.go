//go:build linux

package main

import "github.com/Divaaaan/tenebra/adapters/linux"

// rootDataDir is the machine-scoped profile store for the privileged daemon —
// the Linux analog of the Windows service's %ProgramData%\Tenebra\data and of
// macOS's /Library/Application Support/Tenebra/data. /var/lib is the FHS home
// for persistent state a service owns (as opposed to /run, which is wiped on
// boot and holds only the control socket), and configureSocketPaths locks it to
// root: profiles carry subscription credentials that unprivileged users reach
// through the socket protocol, never the files.
const rootDataDir = "/var/lib/tenebra/data"

// singboxCandidates lists where sing-box may sit on Linux, in probe order. The
// list is the adapter's (adapters/linux.SingboxCandidates) rather than one of
// this package's own, because the runner resolves the same binary from the same
// layout and a second copy of the order would drift from it. See that function
// for why "next to the executable" is only the first of several answers here.
func singboxCandidates(exeDir string) []string {
	return linux.SingboxCandidates(exeDir)
}
