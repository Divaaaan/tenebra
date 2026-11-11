//go:build !linux && !darwin

package installtx

import "net"

func privateRootPeer(net.Conn) bool { return false }
