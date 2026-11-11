//go:build linux || darwin

package securefs

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io/fs"
	"net"
	"os"

	"golang.org/x/sys/unix"
)

func bindUnixSocket(fd int, name, bindPath string, inspect func(int, string) (privateSocketFacts, error)) (net.Listener, privateSocketFacts, error) {
	socket, err := newUnixSocketFD()
	if err != nil {
		return nil, privateSocketFacts{}, err
	}
	file := os.NewFile(uintptr(socket), "securefs-private-socket")
	defer file.Close()
	if err := unix.Bind(socket, &unix.SockaddrUnix{Name: bindPath}); err != nil {
		return nil, privateSocketFacts{}, err
	}
	// Bind succeeded exclusively: only this NEW object is chmodded. An
	// existing unknown socket is never repaired, replaced or unlinked.
	if err := unix.Fchmodat(fd, name, 0600, 0); err != nil {
		return nil, privateSocketFacts{}, err
	}
	if err := unix.Listen(socket, 128); err != nil {
		return nil, privateSocketFacts{}, err
	}
	listener, err := net.FileListener(file)
	if err != nil {
		return nil, privateSocketFacts{}, err
	}
	unixListener, ok := listener.(*net.UnixListener)
	if !ok {
		listener.Close()
		return nil, privateSocketFacts{}, ErrUnsupported
	}
	unixListener.SetUnlinkOnClose(false)
	facts, err := inspect(fd, name)
	if err != nil {
		listener.Close()
		return nil, facts, err
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		listener.Close()
		return nil, facts, err
	}
	pin := "pin_" + hex.EncodeToString(nonce[:])
	if err := unix.Linkat(fd, name, fd, pin, 0); err != nil {
		listener.Close()
		return nil, facts, err
	}
	// The filesystem pin keeps this exact socket inode allocated across an
	// owner crash, even on filesystems with no socket creation timestamp.
	facts, err = inspect(fd, name)
	if err != nil {
		listener.Close()
		return nil, facts, err
	}
	pinned, err := inspect(fd, pin)
	if err != nil || facts.identity.ID != pinned.identity.ID || facts.links != 2 || pinned.links != 2 {
		listener.Close()
		return nil, facts, errors.Join(ErrChanged, err)
	}
	if err := unix.Fsync(fd); err != nil {
		listener.Close()
		return nil, facts, err
	}
	facts.identity.Pin = pin
	return unixListener, facts, nil
}
func publishUnixSocket(fd int, from, to string, expected SocketIdentity,
	inspect func(int, string) (privateSocketFacts, error), rename func(int, string, string) error) (CommitState, error) {
	facts, err := inspect(fd, from)
	if err != nil {
		return Unchanged, err
	}
	pin, pinErr := inspect(fd, expected.Pin)
	if !expected.valid() || facts.identity.ID != expected.ID || pinErr != nil || pin.identity.ID != expected.ID || facts.links != 2 || pin.links != 2 {
		return Unchanged, ErrChanged
	}
	if err := rename(fd, from, to); err != nil {
		return Unchanged, err
	}
	facts, err = inspect(fd, to)
	if err != nil || facts.identity.ID != expected.ID || facts.links != 2 {
		return Indeterminate, errors.Join(ErrChanged, err)
	}
	if err := unix.Fsync(fd); err != nil {
		return Indeterminate, err
	}
	return Committed, nil
}
func removeUnixSocket(fd int, names []string, expected SocketIdentity, inspect func(int, string) (privateSocketFacts, error), actor string) error {
	if !expected.valid() {
		return ErrUntrusted
	}
	pin, pinErr := inspect(fd, expected.Pin)
	var found []string
	policy := &rootState{policy: Policy{Mode: SameAccountPrivate}, actor: actor}
	for _, name := range names {
		facts, err := inspect(fd, name)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		// A missing pin cannot be replaced by matching an inode number.
		if pinErr != nil || pin.identity.ID != expected.ID || facts.identity.ID != expected.ID {
			return ErrChanged
		}
		if err := verifySocketFacts(facts, policy); err != nil {
			return err
		}
		found = append(found, name)
	}
	if errors.Is(pinErr, fs.ErrNotExist) {
		if len(found) != 0 {
			return ErrChanged
		}
		return unix.Fsync(fd)
	}
	if pinErr != nil {
		return pinErr
	}
	if pin.identity.ID != expected.ID {
		return ErrChanged
	}
	if err := verifySocketFacts(pin, policy); err != nil {
		return err
	}
	if pin.links != uint32(len(found)+1) {
		return ErrUntrusted
	} // preserve any unknown extra link
	for _, name := range found {
		if err := unix.Unlinkat(fd, name, 0); err != nil {
			return err
		}
	}
	if err := unix.Unlinkat(fd, expected.Pin, 0); err != nil {
		return err
	}
	return unix.Fsync(fd)
}
