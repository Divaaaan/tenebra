//go:build darwin

package control

import (
	"bufio"
	"fmt"
	"net"
	"os/exec"
	"strings"
)

// networksetup is the macOS CLI that reads and writes per-service proxy settings.
// It needs no admin rights for the current user's network services, so it fits
// the locked-down-machine use case the same way the WinINet registry does.
const networksetup = "/usr/sbin/networksetup"

// enableSystemProxy points the primary network service at hostport for the web
// (HTTP), secure-web (HTTPS), and SOCKS proxies. Setting each also turns it on, so
// the OS routes through the mixed inbound immediately. Applying to one service —
// the one backing the default route — matches how the tunnel path already reasons
// about the primary interface, and avoids stamping proxies onto every inactive
// service.
func enableSystemProxy(hostport string) error {
	host, port, err := net.SplitHostPort(hostport)
	if err != nil {
		return fmt.Errorf("parse proxy address %q: %w", hostport, err)
	}
	svc, err := primaryNetworkService()
	if err != nil {
		return err
	}
	for _, sub := range []string{"-setwebproxy", "-setsecurewebproxy", "-setsocksfirewallproxy"} {
		if err := runNetworksetup(sub, svc, host, port); err != nil {
			return err
		}
	}
	return nil
}

// disableSystemProxy turns the web, secure-web, and SOCKS proxies off on the
// primary service, restoring direct connectivity. It flips only the state flags,
// leaving the stored host/port so a re-enable need not re-supply them.
func disableSystemProxy() error {
	svc, err := primaryNetworkService()
	if err != nil {
		return err
	}
	for _, sub := range []string{"-setwebproxystate", "-setsecurewebproxystate", "-setsocksfirewallproxystate"} {
		if err := runNetworksetup(sub, svc, "off"); err != nil {
			return err
		}
	}
	return nil
}

// readSystemProxy reads the web-proxy state of the primary service for the startup
// reconcile. The web proxy is representative: enableSystemProxy sets all three
// together, so the web entry alone tells the reconcile whether we left a proxy
// pointing at our mixed inbound.
func readSystemProxy() (proxyState, error) {
	svc, err := primaryNetworkService()
	if err != nil {
		return proxyState{}, err
	}
	out, err := exec.Command(networksetup, "-getwebproxy", svc).Output()
	if err != nil {
		return proxyState{}, fmt.Errorf("networksetup -getwebproxy: %w", err)
	}
	return parseGetWebProxy(string(out)), nil
}

// parseGetWebProxy turns `networksetup -getwebproxy` output into a proxyState. The
// output is a set of "Key: Value" lines (Enabled/Server/Port); a missing field
// leaves its zero value.
func parseGetWebProxy(out string) proxyState {
	var st proxyState
	var server, port string
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		key, val, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		val = strings.TrimSpace(val)
		switch strings.TrimSpace(key) {
		case "Enabled":
			st.Enabled = strings.EqualFold(val, "Yes")
		case "Server":
			server = val
		case "Port":
			port = val
		}
	}
	if server != "" && port != "" {
		st.Server = net.JoinHostPort(server, port)
	}
	return st
}

// primaryNetworkService returns the name of the network service backing the
// default route (e.g. "Wi-Fi"), which is the service whose proxy the guard sets.
// It maps the default route's device to a service via the network service order,
// so a Wi-Fi vs Ethernet machine each targets the right one.
func primaryNetworkService() (string, error) {
	dev, err := defaultRouteDevice()
	if err != nil {
		return "", err
	}
	out, err := exec.Command(networksetup, "-listnetworkserviceorder").Output()
	if err != nil {
		return "", fmt.Errorf("networksetup -listnetworkserviceorder: %w", err)
	}
	svc := serviceForDevice(string(out), dev)
	if svc == "" {
		return "", fmt.Errorf("no network service found for default-route device %q", dev)
	}
	return svc, nil
}

// defaultRouteDevice returns the interface the default route uses, e.g. "en0".
func defaultRouteDevice() (string, error) {
	out, err := exec.Command("/sbin/route", "-n", "get", "default").Output()
	if err != nil {
		return "", fmt.Errorf("route get default: %w", err)
	}
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		key, val, ok := strings.Cut(sc.Text(), ":")
		if ok && strings.TrimSpace(key) == "interface" {
			return strings.TrimSpace(val), nil
		}
	}
	return "", fmt.Errorf("no default-route interface in route output")
}

// serviceForDevice finds the service name whose block in
// `networksetup -listnetworkserviceorder` names dev as its Device. The output
// pairs each "(N) ServiceName" line with a following "(Hardware Port: ...,
// Device: enX)" line, so scan for the device and return the service captured just
// before it.
func serviceForDevice(order, dev string) string {
	want := "Device: " + dev + ")"
	sc := bufio.NewScanner(strings.NewReader(order))
	var service string
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "(") && strings.Contains(line, ")") && !strings.Contains(line, "Hardware Port") {
			// "(1) Wi-Fi" -> "Wi-Fi"; skip the leading "(N) " label.
			if i := strings.Index(line, ") "); i >= 0 {
				service = strings.TrimSpace(line[i+2:])
			}
			continue
		}
		if strings.Contains(line, want) {
			return service
		}
	}
	return ""
}

// runNetworksetup runs networksetup with args, wrapping a non-zero exit with the
// command context so a failure names the subcommand that failed.
func runNetworksetup(args ...string) error {
	if err := exec.Command(networksetup, args...).Run(); err != nil {
		return fmt.Errorf("networksetup %s: %w", strings.Join(args, " "), err)
	}
	return nil
}
