//go:build darwin

package securefs

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"unsafe"

	"golang.org/x/sys/unix"
)

type darwinLocation struct {
	parent     handle
	name       string
	ownsParent bool
}
type darwinNative struct {
	mu        sync.Mutex
	uid       int
	rootDev   int32
	rootIno   uint64
	locations map[handle]darwinLocation
}

func newNativeOps() (nativeOps, error) {
	return &darwinNative{uid: os.Geteuid(), locations: make(map[handle]darwinLocation)}, nil
}
func (n *darwinNative) principal() (string, error) { return "uid:" + strconv.Itoa(n.uid), nil }
func darwinFD(h handle) int                        { return int(h) - 1 }
func darwinHandle(fd int) handle                   { return handle(fd + 1) }
func darwinError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, unix.ENOENT):
		return fs.ErrNotExist
	case errors.Is(err, unix.EEXIST):
		return fs.ErrExist
	case errors.Is(err, unix.ELOOP):
		return ErrUntrusted
	case errors.Is(err, unix.EWOULDBLOCK):
		return ErrBusy
	case errors.Is(err, unix.ENOSYS), errors.Is(err, unix.ENOTSUP):
		return errors.Join(ErrUnsupported, err)
	default:
		return err
	}
}
func (n *darwinNative) openVolume(path string) (handle, error) {
	if path != "/" {
		return 0, ErrUnsupported
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return 0, darwinError(err)
	}
	var st unix.Stat_t
	if err = unix.Fstat(fd, &st); err != nil {
		unix.Close(fd)
		return 0, err
	}
	n.mu.Lock()
	n.rootDev, n.rootIno = st.Dev, st.Ino
	n.mu.Unlock()
	return darwinHandle(fd), nil
}
func (n *darwinNative) openAt(parent handle, name string, req openRequest) (handle, error) {
	if _, err := relativeParts(name); err != nil || strings.Contains(name, "/") {
		return 0, ErrUntrusted
	}
	if req.create && (req.policy.Mode == OperatorInput || req.policy.Mode != SameAccountPrivate && n.uid != 0) {
		return 0, ErrUnsupported
	}
	if req.create && req.directory {
		if err := unix.Mkdirat(darwinFD(parent), name, 0700); err != nil {
			return 0, darwinError(err)
		}
	}
	var fd int
	var err error
	if req.create && !req.directory {
		mode := uint32(0600)
		if req.permissions != 0 {
			mode = req.permissions
		}
		if mode != 0600 && mode != 0700 && !(mode == 0644 && req.machineMetadata && req.policy.Mode == MachineContainer && n.uid == 0) {
			return 0, ErrUnsupported
		}
		fd, err = unix.Openat(darwinFD(parent), name, unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_RDWR, mode)
		if err == nil {
			if check := unix.Fchmod(fd, mode); check != nil {
				unix.Close(fd)
				return 0, check
			}
		}
	} else {
		// O_EVTONLY pins identity without reading a special device or waiting
		// for a FIFO. A separate content fd must match this retained identity.
		pinned, check := unix.Openat(darwinFD(parent), name, unix.O_EVTONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if check != nil {
			if !req.create && (errors.Is(check, unix.ELOOP) || errors.Is(check, unix.ENOTDIR)) {
				if alias, ok, aliasErr := n.openSystemAlias(parent, name, req); ok {
					return alias, aliasErr
				}
			}
			return 0, darwinError(check)
		}
		var before unix.Stat_t
		check = unix.Fstat(pinned, &before)
		kind := before.Mode & unix.S_IFMT
		if check != nil || kind != unix.S_IFREG && kind != unix.S_IFDIR || req.directory && kind != unix.S_IFDIR || !req.directory && !req.any && kind != unix.S_IFREG {
			unix.Close(pinned)
			if check != nil {
				return 0, check
			}
			return 0, ErrUntrusted
		}
		if req.create && req.directory {
			// Mkdirat succeeded exclusively; this is a new object, never a
			// permission repair of an existing or caller-owned source.
			if check = unix.Fchmod(pinned, 0700); check != nil {
				unix.Close(pinned)
				return 0, check
			}
		}
		flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK
		if req.write {
			flags = unix.O_RDWR | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK
		}
		if kind == unix.S_IFDIR {
			flags |= unix.O_DIRECTORY
		}
		fd, err = unix.Openat(darwinFD(parent), name, flags, 0)
		if err == nil {
			var after unix.Stat_t
			if check = unix.Fstat(fd, &after); check != nil || before.Dev != after.Dev || before.Ino != after.Ino || before.Mode&unix.S_IFMT != after.Mode&unix.S_IFMT {
				unix.Close(fd)
				fd = -1
				if check != nil {
					err = check
				} else {
					err = ErrChanged
				}
			}
		}
		unix.Close(pinned)
	}
	if err != nil {
		return 0, darwinError(err)
	}
	h := darwinHandle(fd)
	n.mu.Lock()
	n.locations[h] = darwinLocation{parent: parent, name: name}
	n.mu.Unlock()
	return h, nil
}

// Only the verified volume root may traverse the three fixed OS aliases.
// Their literal kernel link target must match, and /private is opened and
// verified through retained descriptors. No general realpath/ls fallback exists.
func (n *darwinNative) openSystemAlias(parent handle, name string, req openRequest) (handle, bool, error) {
	if name != "var" && name != "tmp" && name != "etc" {
		return 0, false, nil
	}
	var st unix.Stat_t
	if err := unix.Fstat(darwinFD(parent), &st); err != nil {
		return 0, true, err
	}
	n.mu.Lock()
	isRoot := st.Dev == n.rootDev && st.Ino == n.rootIno
	n.mu.Unlock()
	if !isRoot {
		return 0, false, nil
	}
	buffer := make([]byte, 128)
	size, err := unix.Readlinkat(darwinFD(parent), name, buffer)
	if err != nil || size == len(buffer) || !darwinSystemAlias(name, string(buffer[:size])) {
		return 0, true, ErrUntrusted
	}
	private, err := n.openAt(parent, "private", openRequest{directory: true})
	if err != nil {
		return 0, true, err
	}
	info, err := n.inspect(private)
	if err == nil {
		err = verifyObject(info, Policy{Mode: MachineContainer}, "uid:0", true)
	}
	if err != nil {
		n.close(private)
		return 0, true, err
	}
	child, err := n.openAt(private, name, req)
	if err != nil {
		n.close(private)
		return 0, true, err
	}
	n.mu.Lock()
	location := n.locations[child]
	location.ownsParent = true
	n.locations[child] = location
	n.mu.Unlock()
	return child, true, nil
}

// fgetattrlist is deliberately a local syscall wrapper: x/sys v0.41.0 has the
// Attrlist ABI and syscall number, but no invented Fgetattrlist function.
func darwinGetAttributes(h handle, attrs unix.Attrlist, capacity int) ([]byte, error) {
	if capacity < 4 || capacity > darwinSecurityLimit {
		return nil, ErrLimit
	}
	data := make([]byte, capacity)
	_, _, errno := unix.Syscall6(unix.SYS_FGETATTRLIST, uintptr(darwinFD(h)), uintptr(unsafe.Pointer(&attrs)), uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)), unix.FSOPT_REPORT_FULLSIZE, 0)
	runtime.KeepAlive(&attrs)
	runtime.KeepAlive(data)
	if errno != 0 {
		return nil, errors.Join(ErrUnsupported, errno)
	}
	size := uint64(data[0]) | uint64(data[1])<<8 | uint64(data[2])<<16 | uint64(data[3])<<24
	if size < 4 || size > uint64(len(data)) {
		return nil, ErrLimit
	}
	return data[:int(size)], nil
}
func darwinReadSecurity(h handle) (securityFacts, error) {
	volume, err := darwinGetAttributes(h, unix.Attrlist{Bitmapcount: 5, Volattr: unix.ATTR_VOL_INFO | unix.ATTR_VOL_CAPABILITIES}, 36)
	if err != nil {
		return securityFacts{}, err
	}
	if !darwinVolumeHasSecurity(volume) {
		return securityFacts{}, ErrUnsupported
	}
	var st unix.Stat_t
	if err := unix.Fstat(darwinFD(h), &st); err != nil {
		return securityFacts{}, err
	}
	data, err := darwinGetAttributes(h, unix.Attrlist{Bitmapcount: 5, Commonattr: unix.ATTR_CMN_EXTENDED_SECURITY}, darwinSecurityLimit)
	if err != nil {
		return securityFacts{}, err
	}
	return parseDarwinSecurity(data, st.Uid)
}
func (n *darwinNative) inspect(h handle) (objectInfo, error) {
	var st, after unix.Stat_t
	if err := unix.Fstat(darwinFD(h), &st); err != nil {
		return objectInfo{}, err
	}
	security, err := darwinReadSecurity(h)
	if err != nil {
		return objectInfo{}, err
	}
	if err := unix.Fstat(darwinFD(h), &after); err != nil {
		return objectInfo{}, err
	}
	if st.Dev != after.Dev || st.Ino != after.Ino || st.Ctim != after.Ctim || st.Mode != after.Mode || st.Uid != after.Uid || st.Gid != after.Gid || st.Size != after.Size {
		return objectInfo{}, ErrChanged
	}
	info := objectInfo{id: fmt.Sprintf("%d:%d", st.Dev, st.Ino), owner: fmt.Sprintf("uid:%d", st.Uid),
		change: fmt.Sprintf("%d:%d:%d:%d", st.Mtim.Sec, st.Mtim.Nsec, st.Ctim.Sec, st.Ctim.Nsec), links: uint32(st.Nlink), size: st.Size,
		securityKnown: true, sticky: st.Mode&unix.S_ISVTX != 0, grants: security.grants}
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFDIR:
		info.kind = kindDirectory
	case unix.S_IFREG:
		info.kind = kindRegular
	case unix.S_IFLNK:
		info.kind = kindLink
	default:
		info.kind = kindSpecial
	}
	for _, entry := range []struct {
		mask      uint16
		principal string
	}{{0070, "posix:group"}, {0007, "posix:other"}} {
		bits := st.Mode & entry.mask
		if entry.mask == 0070 {
			bits >>= 3
		}
		var rights accessRights
		if bits&5 != 0 {
			rights |= rightRead
		}
		if bits&2 != 0 {
			rights |= rightMutate | rightCreate
		}
		info.grants = append(info.grants, grant{principal: entry.principal, rights: rights})
	}
	return info, nil
}
func (n *darwinNative) read(h handle, b []byte) (int, error) {
	count, err := unix.Read(darwinFD(h), b)
	if count == 0 && err == nil {
		err = io.EOF
	}
	return count, err
}
func (n *darwinNative) write(h handle, b []byte) (int, error) { return unix.Write(darwinFD(h), b) }
func (n *darwinNative) flush(h handle) error {
	if err := unix.Fsync(darwinFD(h)); err != nil {
		return darwinError(err)
	}
	var st unix.Stat_t
	if err := unix.Fstat(darwinFD(h), &st); err != nil {
		return err
	}
	if st.Mode&unix.S_IFMT == unix.S_IFREG {
		_, err := unix.FcntlInt(uintptr(darwinFD(h)), unix.F_FULLFSYNC, 0)
		return darwinError(err)
	}
	return nil
}
func (n *darwinNative) path(h handle) (string, error) {
	buffer := make([]byte, unix.PathMax)
	_, _, errno := unix.Syscall6(unix.SYS_FCNTL, uintptr(darwinFD(h)), unix.F_GETPATH, uintptr(unsafe.Pointer(&buffer[0])), 0, 0, 0)
	runtime.KeepAlive(buffer)
	if errno != 0 {
		return "", darwinError(errno)
	}
	for i, b := range buffer {
		if b == 0 {
			path := string(buffer[:i])
			if !strings.HasPrefix(path, "/") {
				return "", ErrChanged
			}
			return path, nil
		}
	}
	return "", ErrLimit
}
func (n *darwinNative) list(h handle) ([]string, error) {
	fd, err := unix.Openat(darwinFD(h), ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, darwinError(err)
	}
	file := os.NewFile(uintptr(fd), "securefs-directory")
	defer file.Close()
	var names []string
	for {
		batch, err := file.Readdirnames(256)
		for _, name := range batch {
			if name == "." || name == ".." {
				continue
			}
			if _, err := relativeParts(name); err != nil || strings.Contains(name, "/") {
				return nil, ErrUntrusted
			}
			names = append(names, name)
			if len(names) > 100000 {
				return nil, ErrLimit
			}
		}
		if errors.Is(err, io.EOF) {
			return names, nil
		}
		if err != nil {
			return nil, err
		}
	}
}
func (n *darwinNative) location(h handle) (darwinLocation, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	location, ok := n.locations[h]
	if !ok {
		return darwinLocation{}, ErrChanged
	}
	return location, nil
}
func (n *darwinNative) checkName(h, parent handle, name string) error {
	var original, current unix.Stat_t
	if err := unix.Fstat(darwinFD(h), &original); err != nil {
		return err
	}
	if err := unix.Fstatat(darwinFD(parent), name, &current, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return darwinError(err)
	}
	if original.Dev != current.Dev || original.Ino != current.Ino {
		return ErrChanged
	}
	return nil
}
func (n *darwinNative) rename(source, parent handle, name string, replace bool) error {
	location, err := n.location(source)
	if err != nil {
		return err
	}
	if location.ownsParent {
		return ErrUnsupported
	}
	if err := n.checkName(source, location.parent, location.name); err != nil {
		return err
	}
	flags := uint32(0)
	if !replace {
		flags = unix.RENAME_EXCL
	}
	if err := unix.RenameatxNp(darwinFD(location.parent), location.name, darwinFD(parent), name, flags); err != nil {
		return darwinError(err)
	}
	n.mu.Lock()
	n.locations[source] = darwinLocation{parent: parent, name: name}
	n.mu.Unlock()
	return darwinError(unix.Fsync(darwinFD(parent)))
}
func (n *darwinNative) replacePrivate(parent, temp handle, temporary, target string) (CommitState, error) {
	if err := n.checkName(temp, parent, temporary); err != nil {
		return Unchanged, err
	}
	if err := unix.Renameat(darwinFD(parent), temporary, darwinFD(parent), target); err != nil {
		return Unchanged, darwinError(err)
	}
	n.mu.Lock()
	n.locations[temp] = darwinLocation{parent: parent, name: target}
	n.mu.Unlock()
	if err := unix.Fsync(darwinFD(parent)); err != nil {
		return Indeterminate, darwinError(err)
	}
	return Committed, nil
}
func (n *darwinNative) removeOwned(source, parent handle, name string) error {
	location, err := n.location(source)
	if err != nil {
		return err
	}
	if location.ownsParent {
		return ErrUnsupported
	}
	if err := n.checkName(source, parent, name); err != nil {
		return err
	}
	var st unix.Stat_t
	if err := unix.Fstat(darwinFD(source), &st); err != nil {
		return err
	}
	flags := 0
	if st.Mode&unix.S_IFMT == unix.S_IFDIR {
		flags = unix.AT_REMOVEDIR
	}
	if err := unix.Unlinkat(darwinFD(parent), name, flags); err != nil {
		return darwinError(err)
	}
	return darwinError(unix.Fsync(darwinFD(parent)))
}
func (n *darwinNative) containQuarantined(h handle, policy Policy) error {
	location, err := n.location(h)
	if err != nil {
		return err
	}
	actor, _ := n.principal()
	return verifyForensicContainment(n, h, location.parent, policy, actor)
}
func (n *darwinNative) lock(h handle) error {
	return darwinError(unix.Flock(darwinFD(h), unix.LOCK_EX|unix.LOCK_NB))
}
func (n *darwinNative) unlock(h handle) error {
	return darwinError(unix.Flock(darwinFD(h), unix.LOCK_UN))
}
func (n *darwinNative) close(h handle) error {
	n.mu.Lock()
	location := n.locations[h]
	delete(n.locations, h)
	n.mu.Unlock()
	err := unix.Close(darwinFD(h))
	if location.ownsParent {
		err = errors.Join(err, n.close(location.parent))
	}
	return err
}
