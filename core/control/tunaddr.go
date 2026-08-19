package control

import (
	"fmt"
	"net"

	"github.com/Divaaaan/tenebra/core/singbox"
)

// pickFreeTunAddress chooses a tun address that nothing else on the machine is
// already using, and records it for the next config build.
//
// The address used to be a constant, and it is not ours alone: Hiddify gives its
// own tun 172.19.0.1 too. Two adapters cannot hold one address — the second to
// start fails to configure itself and its sing-box exits within seconds, while
// the daemon goes on reporting "connected" over a tunnel that no longer exists.
// That is the failure this fixes, observed on the author's machine and the
// likeliest way this app meets a VPN the user already had installed.
//
// Called before each connect rather than once at startup: what is free changes
// as other clients come and go, and the answer only matters at the moment a tun
// is about to be raised.
func (d *Daemon) pickFreeTunAddress() {
	addrs := d.localAddrs()
	chosen := singbox.FreeTunAddress(addrs)

	d.mu.Lock()
	previous := d.tun.Address
	d.tun.Address = chosen
	d.mu.Unlock()

	if previous != "" && previous != chosen {
		d.emitLog(LogInfo, fmt.Sprintf("туннель переезжает на %s — прежний адрес занят", chosen))
	}
}

// defaultLocalAddrs reports the machine's interface addresses. Injectable so a
// test can describe a machine with a neighbour already holding the default.
func defaultLocalAddrs() []net.Addr { return singbox.LocalAddrs() }
