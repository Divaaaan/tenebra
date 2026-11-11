// Package securefs keeps privileged filesystem access bound to verified objects.
package securefs

import (
	"errors"
	"io/fs"
)

type handle uintptr
type objectKind uint8

const (
	kindDirectory objectKind = iota + 1
	kindRegular
	kindLink
	kindSpecial
)

type accessRights uint8

const (
	rightRead accessRights = 1 << iota
	rightMutate
	rightCreate
)

type grant struct {
	principal                  string
	rights                     accessRights
	inheritOnly, deny, unknown bool
}

type objectInfo struct {
	id, owner, change string
	kind              objectKind
	links             uint32
	size              int64
	securityKnown     bool
	sticky            bool
	grants            []grant
}

type openRequest struct {
	directory, create, write, delete, freeze, any bool
	permissions                                   uint32
	machineMetadata                               bool
	policy                                        Policy
}

// The extra identity/path queries return kernel facts, never browser claims.
// Paths are output only; all access below remains relative to retained handles.
type nativeOps interface {
	principal() (string, error)
	openVolume(string) (handle, error)
	openAt(handle, string, openRequest) (handle, error)
	inspect(handle) (objectInfo, error)
	read(handle, []byte) (int, error)
	write(handle, []byte) (int, error)
	flush(handle) error
	list(handle) ([]string, error)
	rename(handle, handle, string, bool) error
	replacePrivate(handle, handle, string, string) (CommitState, error)
	removeOwned(handle, handle, string) error
	containQuarantined(handle, Policy) error
	lock(handle) error
	unlock(handle) error
	path(handle) (string, error)
	close(handle) error
}

func nativeError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return fs.ErrNotExist
	}
	return err
}
