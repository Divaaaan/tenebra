//go:build linux

package securefs

import (
	"fmt"
	"net"
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

func newUnixSocketFD() (int, error) {
	return unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
}
func linuxSocketFacts(fd int, name string) (privateSocketFacts, error) {
	var st unix.Stat_t
	if err := unix.Fstatat(fd, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return privateSocketFacts{}, linuxError(err)
	}
	if st.Mode&unix.S_IFMT != unix.S_IFSOCK || st.Nlink < 1 || st.Nlink > 3 {
		return privateSocketFacts{}, ErrUntrusted
	}
	return privateSocketFacts{identity: SocketIdentity{ID: fmt.Sprintf("%d:%d", st.Dev, st.Ino)},
		owner: fmt.Sprintf("uid:%d", st.Uid), mode: uint32(st.Mode) & 07777, links: uint32(st.Nlink), known: true}, nil
}
func (n *linuxNative) bindPrivateSocket(parent handle, name string) (net.Listener, privateSocketFacts, error) {
	proc, err := n.procFD()
	if err != nil {
		return nil, privateSocketFacts{}, err
	}
	defer unix.Close(proc)
	// The process-fd namespace was verified as kernel procfs. The parent fd is
	// retained by Root; no mutable source/parent pathname supplies authority.
	path := "/proc/" + strconv.Itoa(os.Getpid()) + "/fd/" + strconv.Itoa(unixFD(parent)) + "/" + name
	return bindUnixSocket(unixFD(parent), name, path, linuxSocketFacts)
}
func (n *linuxNative) privateSocketFacts(parent handle, name string) (privateSocketFacts, error) {
	return linuxSocketFacts(unixFD(parent), name)
}
func (n *linuxNative) publishPrivateSocket(parent handle, from, to string, identity SocketIdentity) (CommitState, error) {
	state, err := publishUnixSocket(unixFD(parent), from, to, identity, linuxSocketFacts, func(fd int, from, to string) error {
		return linuxError(unix.Renameat2(fd, from, fd, to, unix.RENAME_NOREPLACE))
	})
	return state, linuxError(err)
}
func (n *linuxNative) removePrivateSocket(parent handle, names []string, identity SocketIdentity) error {
	actor, _ := n.principal()
	return linuxError(removeUnixSocket(unixFD(parent), names, identity, linuxSocketFacts, actor))
}
