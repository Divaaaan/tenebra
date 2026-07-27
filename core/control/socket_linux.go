//go:build linux

package control

// DefaultSocketPath is the unix domain socket the control protocol is served on
// when the core runs detached from the UI on Linux — as the system service that
// owns the tunnel, or via `tenebra-core --socket` from a shell for development.
// It is the Linux analog of PipeName: one well-known path the UI discovers the
// core by dialling, so the desktop shell can hard-code the same string.
//
// /run is the FHS home for volatile runtime state: root-owned, a tmpfs on every
// modern distribution, and cleared on boot — which means a socket file left
// behind by a hard kill never survives a reboot (ListenSocket also clears a
// stale one within a boot). /var/run is a compatibility symlink onto it, so the
// canonical spelling is the one used here; macOS keeps the /var/run form
// because that is the real directory there.
//
// The daemon that binds this path runs as root — opening /dev/net/tun and
// installing auto_route's routing rules needs CAP_NET_ADMIN — while the GUI
// that drives it does not. The bind is therefore opened up to every local user
// by the 0666 clamp in ListenSocket and narrowed again, per connection, by the
// SO_PEERCRED check in authorizePeer.
const DefaultSocketPath = "/run/tenebra.sock"
