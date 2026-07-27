//go:build darwin

package control

// DefaultSocketPath is the unix domain socket the control protocol is served on
// when the core runs detached from the UI on macOS — as a root LaunchDaemon, or
// via `tenebra-core --socket` from a shell for development. It is the darwin
// analog of PipeName: one well-known path the UI discovers the core by dialling.
// It lives under /var/run (root-owned) because the daemon that binds it is root;
// the bind is opened up to every local user by the 0666 clamp in ListenSocket
// and narrowed again, per connection, by the peer check in authorizePeer.
const DefaultSocketPath = "/var/run/tenebra.sock"
