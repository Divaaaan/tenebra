//go:build linux

package installtx

import (
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

var errReadyConnectionRefused = unix.ECONNREFUSED

func rootPeerFD(fd int) bool {
	cred, err := unix.GetsockoptUcred(fd, unix.SOL_SOCKET, unix.SO_PEERCRED)
	return err == nil && cred.Uid == 0
}

// ProcessStartToken binds an identified PID to this kernel process lifetime.
// /proc is kernel-owned; no executable path or caller-supplied source is read.
func ProcessStartToken(pid uint32) (string, error) {
	if pid == 0 {
		return "", ErrReadiness
	}
	data, err := os.ReadFile("/proc/" + strconv.FormatUint(uint64(pid), 10) + "/stat")
	if err != nil {
		return "", err
	}
	end := strings.LastIndex(string(data), ")")
	if end < 0 {
		return "", ErrReadiness
	}
	fields := strings.Fields(string(data[end+1:]))
	if len(fields) <= 19 || oneOf(fields[0], "Z", "X", "x") {
		return "", ErrReadiness
	}
	if _, err := strconv.ParseUint(fields[19], 10, 64); err != nil {
		return "", ErrReadiness
	}
	return fields[19], nil
}
func verifyReadyPeerFD(fd int, expected ProcessIdentity) error {
	cred, err := unix.GetsockoptUcred(fd, unix.SOL_SOCKET, unix.SO_PEERCRED)
	if err != nil || cred.Uid != 0 || cred.Pid <= 0 || uint32(cred.Pid) != expected.PID {
		return ErrReadiness
	}
	token, err := ProcessStartToken(expected.PID)
	if err != nil || token != expected.StartToken {
		return ErrReadiness
	}
	return nil
}
