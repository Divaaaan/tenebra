//go:build windows

package control

import (
	"fmt"
	"strings"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// inetSettingsKey is the per-user WinINet configuration key. Writing ProxyEnable
// and ProxyServer here is exactly what the Internet Options dialog does, so it
// applies without admin rights — the whole point of system-proxy mode on a
// locked-down machine.
const inetSettingsKey = `Software\Microsoft\Windows\CurrentVersion\Internet Settings`

// WinINet InternetSetOption codes, from Wininet.h. SETTINGS_CHANGED tells running
// processes the proxy config changed; REFRESH makes them reload it, so an open
// browser honours the new setting without a restart.
const (
	internetOptionSettingsChanged = 39
	internetOptionRefresh         = 37
)

var (
	modWininet            = windows.NewLazySystemDLL("wininet.dll")
	procInternetSetOption = modWininet.NewProc("InternetSetOptionW")
)

// enableSystemProxy points the current user's WinINet proxy at hostport for all
// protocols and refreshes live so open apps pick it up without a restart.
func enableSystemProxy(hostport string) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, inetSettingsKey, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("open Internet Settings: %w", err)
	}
	defer k.Close()
	// ProxyServer as a bare host:port applies to every protocol (HTTP/HTTPS), which
	// is what the mixed inbound serves.
	if err := k.SetStringValue("ProxyServer", hostport); err != nil {
		return fmt.Errorf("set ProxyServer: %w", err)
	}
	if err := k.SetDWordValue("ProxyEnable", 1); err != nil {
		return fmt.Errorf("set ProxyEnable: %w", err)
	}
	return refreshWinINet()
}

// disableSystemProxy turns the current user's WinINet proxy off and refreshes. It
// leaves ProxyServer in place — harmless once ProxyEnable is 0 — so this touches
// only the flag, minimising what the guard rewrites.
func disableSystemProxy() error {
	k, err := registry.OpenKey(registry.CURRENT_USER, inetSettingsKey, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("open Internet Settings: %w", err)
	}
	defer k.Close()
	if err := k.SetDWordValue("ProxyEnable", 0); err != nil {
		return fmt.Errorf("clear ProxyEnable: %w", err)
	}
	return refreshWinINet()
}

// readSystemProxy reads ProxyEnable/ProxyServer for the startup reconcile. A
// missing value reads as off/empty rather than an error, so a machine that never
// had a proxy set is simply "not enabled".
func readSystemProxy() (proxyState, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, inetSettingsKey, registry.QUERY_VALUE)
	if err != nil {
		return proxyState{}, fmt.Errorf("open Internet Settings: %w", err)
	}
	defer k.Close()

	enable, _, err := k.GetIntegerValue("ProxyEnable")
	if err != nil && err != registry.ErrNotExist {
		return proxyState{}, fmt.Errorf("read ProxyEnable: %w", err)
	}
	server, _, err := k.GetStringValue("ProxyServer")
	if err != nil && err != registry.ErrNotExist {
		return proxyState{}, fmt.Errorf("read ProxyServer: %w", err)
	}
	return proxyState{Enabled: enable == 1, Server: firstProxyTarget(server)}, nil
}

// firstProxyTarget extracts a bare host:port from a WinINet ProxyServer value.
// The value is either a single "host:port" (what enableSystemProxy writes) or a
// per-protocol list like "http=127.0.0.1:2080;https=127.0.0.1:2080"; the reconcile
// only needs one target to compare, so strip any "scheme=" prefix and take the
// first entry. A plain value passes through unchanged.
func firstProxyTarget(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	first := v
	if i := strings.IndexByte(first, ';'); i >= 0 {
		first = first[:i]
	}
	if i := strings.IndexByte(first, '='); i >= 0 {
		first = first[i+1:]
	}
	return strings.TrimSpace(first)
}

// refreshWinINet broadcasts the settings-changed and refresh options so running
// processes reload the proxy configuration immediately. A zero return from
// InternetSetOption signals failure; the accompanying error is the last-call
// error only then.
func refreshWinINet() error {
	if r, _, err := procInternetSetOption.Call(0, internetOptionSettingsChanged, 0, 0); r == 0 {
		return fmt.Errorf("InternetSetOption(SETTINGS_CHANGED): %w", err)
	}
	if r, _, err := procInternetSetOption.Call(0, internetOptionRefresh, 0, 0); r == 0 {
		return fmt.Errorf("InternetSetOption(REFRESH): %w", err)
	}
	return nil
}
