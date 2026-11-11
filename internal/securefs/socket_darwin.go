//go:build darwin

package securefs

import (
	"fmt"
	"net"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

func newUnixSocketFD() (int, error) {
	// Darwin has no SOCK_CLOEXEC flag. Coordinate with Go's fork/exec lock
	// until close-on-exec is set, so a concurrent child cannot inherit this fd.
	syscall.ForkLock.RLock()
	defer syscall.ForkLock.RUnlock()
	fd, err := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err == nil {
		unix.CloseOnExec(fd)
	}
	return fd, err
}
func darwinSocketFacts(fd int, name string) (privateSocketFacts, error) {
	h, err := unix.Openat(fd, name, unix.O_EVTONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return privateSocketFacts{}, darwinError(err)
	}
	defer unix.Close(h)
	var st, after unix.Stat_t
	if err := unix.Fstat(h, &st); err != nil {
		return privateSocketFacts{}, err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFSOCK || st.Nlink < 1 || st.Nlink > 3 {
		return privateSocketFacts{}, ErrUntrusted
	}
	security, err := darwinReadSecurity(darwinHandle(h))
	if err != nil {
		return privateSocketFacts{}, err
	}
	if err := unix.Fstat(h, &after); err != nil {
		return privateSocketFacts{}, err
	}
	if st.Dev != after.Dev || st.Ino != after.Ino || st.Ctim != after.Ctim || st.Btim != after.Btim || st.Mode != after.Mode || st.Uid != after.Uid {
		return privateSocketFacts{}, ErrChanged
	}
	return privateSocketFacts{identity: SocketIdentity{ID: fmt.Sprintf("%d:%d", uint32(st.Dev), st.Ino)},
		owner: fmt.Sprintf("uid:%d", st.Uid), mode: uint32(st.Mode) & 07777, links: uint32(st.Nlink), known: true, grants: security.grants}, nil
}
func (n *darwinNative) bindPrivateSocket(parent handle, name string) (net.Listener, privateSocketFacts, error) {
	directory, err := n.path(parent)
	if err != nil {
		return nil, privateSocketFacts{}, err
	}
	// Darwin's bind has no bindat. Only the retained, ACL-verified private
	// root's kernel path is used; stat/rename/unlink remain parent-fd-relative.
	return bindUnixSocket(darwinFD(parent), name, filepath.Join(directory, name), darwinSocketFacts)
}
func (n *darwinNative) privateSocketFacts(parent handle, name string) (privateSocketFacts, error) {
	return darwinSocketFacts(darwinFD(parent), name)
}
func (n *darwinNative) publishPrivateSocket(parent handle, from, to string, identity SocketIdentity) (CommitState, error) {
	state, err := publishUnixSocket(darwinFD(parent), from, to, identity, darwinSocketFacts, func(fd int, from, to string) error {
		return darwinError(unix.RenameatxNp(fd, from, fd, to, unix.RENAME_EXCL))
	})
	return state, darwinError(err)
}
func (n *darwinNative) removePrivateSocket(parent handle, names []string, identity SocketIdentity) error {
	actor, _ := n.principal()
	return darwinError(removeUnixSocket(darwinFD(parent), names, identity, darwinSocketFacts, actor))
}
