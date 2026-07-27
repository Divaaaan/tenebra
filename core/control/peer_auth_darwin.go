//go:build darwin

package control

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"syscall"

	"golang.org/x/sys/unix"
)

// peerCredUID reads the connected peer's effective uid from a unix-domain socket
// via getsockopt(SOL_LOCAL, LOCAL_PEERCRED), which returns the credentials of
// the process on the other end at connect time. It returns ok=false for any conn
// that is not a live unix socket (e.g. a net.Pipe used in tests) or when the
// getsockopt fails, leaving the caller to take the fail-open path.
func peerCredUID(conn net.Conn) (int, bool) {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return 0, false
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, false
	}
	var uid int
	var innerErr error
	ctrlErr := raw.Control(func(fd uintptr) {
		xucred, e := unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
		if e != nil {
			innerErr = e
			return
		}
		uid = int(xucred.Uid)
	})
	if ctrlErr != nil || innerErr != nil {
		return 0, false
	}
	return uid, true
}

// consoleUserUID reports the uid of the macOS console user as a decimal string.
// It reads the owner of /dev/console, which the window server chowns to whoever
// is logged in at the physical display — the account the GUI runs as. A stat
// failure (or a device with no owner info) is returned as an error so the policy
// falls open rather than locking the GUI out on a lookup hiccup.
func consoleUserUID() (string, error) {
	fi, err := os.Stat("/dev/console")
	if err != nil {
		return "", err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return "", fmt.Errorf("control: /dev/console carries no stat info")
	}
	return strconv.FormatUint(uint64(st.Uid), 10), nil
}
