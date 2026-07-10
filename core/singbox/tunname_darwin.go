//go:build darwin

package singbox

// platformTUNName is the default tun interface name on macOS: empty. Unlike
// Windows' wintun, which accepts any adapter name, macOS reserves the tun
// namespace to the kernel's utun<N> pattern — sing-box rejects any other name
// ("bad tun name") and, given no name, claims the next free utun itself. Leaving
// it empty is therefore the only portable default; a caller that needs a
// specific device can still pass an explicit "utun<N>" through TunOptions.
const platformTUNName = ""
