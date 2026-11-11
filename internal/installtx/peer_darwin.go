//go:build darwin

package installtx

import (
	"strconv"

	"golang.org/x/sys/unix"
)

var errReadyConnectionRefused = unix.ECONNREFUSED

func rootPeerFD(fd int) bool {
	cred, err := unix.GetsockoptXucred(fd, unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
	return err == nil && cred.Uid == 0
}

// XNU bsd/sys/proc.h defines SZOMB=5; x/sys v0.41.0 does not export it.
// https://github.com/apple-oss-distributions/xnu/blob/main/bsd/sys/proc.h
const darwinZombieState = 5

func ProcessStartToken(pid uint32) (string, error) {
	if pid == 0 || pid > 1<<31-1 {
		return "", ErrReadiness
	}
	info, err := unix.SysctlKinfoProc("kern.proc.pid", int(pid))
	if err != nil || info.Proc.P_pid != int32(pid) || info.Proc.P_stat == darwinZombieState {
		return "", ErrReadiness
	}
	start := info.Proc.P_starttime
	if start.Sec <= 0 || start.Usec < 0 || start.Usec >= 1000000 {
		return "", ErrReadiness
	}
	return strconv.FormatInt(start.Sec, 10) + ":" + strconv.FormatInt(int64(start.Usec), 10), nil
}
func verifyReadyPeerFD(fd int, expected ProcessIdentity) error {
	cred, err := unix.GetsockoptXucred(fd, unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
	if err != nil || cred.Uid != 0 {
		return ErrReadiness
	}
	pid, err := unix.GetsockoptInt(fd, unix.SOL_LOCAL, unix.LOCAL_PEERPID)
	if err != nil || pid <= 0 || uint32(pid) != expected.PID {
		return ErrReadiness
	}
	token, err := ProcessStartToken(expected.PID)
	if err != nil || token != expected.StartToken {
		return ErrReadiness
	}
	return nil
}
