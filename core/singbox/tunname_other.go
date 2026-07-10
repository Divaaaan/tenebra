//go:build !darwin

package singbox

// platformTUNName is the default tun interface name off macOS. Windows' wintun
// and Linux both accept an arbitrary name, so the tunnel gets the branded
// "tenebra" — stable across runs and easy to spot in adapter lists. macOS is the
// exception (see tunname_darwin.go), where the kernel dictates utun<N>.
const platformTUNName = "tenebra"
