//go:build linux

package securefs

import "golang.org/x/sys/unix"

func (n *linuxNative) verifyRunningImage(expected handle) error {
	proc, err := n.procFD()
	if err != nil {
		return err
	}
	defer unix.Close(proc)
	// proc is the verified kernel /proc/<numeric SELF pid>/fd directory.
	// This ONE compiled kernel link names the running text image, including
	// its old inode after replacement. No request-controlled link is followed.
	image, err := unix.Openat(proc, "../exe", unix.O_PATH|unix.O_CLOEXEC, 0)
	if err != nil {
		return linuxError(err)
	}
	defer unix.Close(image)
	var actual, role unix.Stat_t
	if err := unix.Fstat(image, &actual); err != nil {
		return err
	}
	if err := unix.Fstat(unixFD(expected), &role); err != nil {
		return err
	}
	if actual.Mode&unix.S_IFMT != unix.S_IFREG || role.Mode&unix.S_IFMT != unix.S_IFREG || actual.Dev != role.Dev || actual.Ino != role.Ino {
		return ErrChanged
	}
	return nil
}
