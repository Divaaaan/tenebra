//go:build linux

package securefs

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

type linuxLocation struct {
	parent handle
	name   string
}
type linuxNative struct {
	mu        sync.Mutex
	uid       int
	locations map[handle]linuxLocation
}

func newNativeOps() (nativeOps, error) {
	return &linuxNative{uid: os.Geteuid(), locations: map[handle]linuxLocation{}}, nil
}
func (n *linuxNative) principal() (string, error) { return "uid:" + strconv.Itoa(n.uid), nil }
func unixFD(h handle) int                         { return int(h) - 1 }
func unixHandle(fd int) handle                    { return handle(fd + 1) }
func linuxError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, unix.ENOENT) {
		return fs.ErrNotExist
	}
	if errors.Is(err, unix.EEXIST) {
		return fs.ErrExist
	}
	if errors.Is(err, unix.ELOOP) {
		return ErrUntrusted
	}
	if errors.Is(err, unix.EWOULDBLOCK) {
		return ErrBusy
	}
	return err
}
func (n *linuxNative) openVolume(path string) (handle, error) {
	if path != "/" {
		return 0, ErrUnsupported
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	return unixHandle(fd), linuxError(err)
}
func (n *linuxNative) procFD() (int, error) {
	// No arbitrary realpath or mutable /proc/self symlink supplies authority.
	fd, err := unix.Open("/proc", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return -1, ErrUnsupported
	}
	var info unix.Statfs_t
	if err := unix.Fstatfs(fd, &info); err != nil || info.Type != unix.PROC_SUPER_MAGIC {
		unix.Close(fd)
		return -1, ErrUnsupported
	}
	process, err := unix.Openat(fd, strconv.Itoa(os.Getpid()), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	unix.Close(fd)
	if err != nil {
		return -1, ErrUnsupported
	}
	result, err := unix.Openat(process, "fd", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	unix.Close(process)
	if err != nil {
		return -1, ErrUnsupported
	}
	return result, nil
}
func (n *linuxNative) openAt(parent handle, name string, req openRequest) (handle, error) {
	if _, err := relativeParts(name); err != nil || strings.Contains(name, "/") {
		return 0, ErrUntrusted
	}
	if req.create && req.policy.Mode != SameAccountPrivate && n.uid != 0 {
		return 0, ErrUnsupported
	}
	if req.create && req.policy.Mode == OperatorInput {
		return 0, ErrUnsupported
	}
	if req.create && req.directory {
		if err := unix.Mkdirat(unixFD(parent), name, 0o700); err != nil {
			return 0, linuxError(err)
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
		fd, err = unix.Openat(unixFD(parent), name, unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_RDWR, mode)
		if err == nil {
			// O_EXCL proves this is our newly created private object. Correct
			// creation's umask here; never chmod an existing untrusted object.
			if check := unix.Fchmod(fd, mode); check != nil {
				unix.Close(fd)
				return 0, check
			}
		}
	} else {
		pinned, openErr := unix.Openat(unixFD(parent), name, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if openErr != nil {
			return 0, linuxError(openErr)
		}
		var before unix.Stat_t
		if err := unix.Fstat(pinned, &before); err != nil {
			unix.Close(pinned)
			return 0, err
		}
		kind := before.Mode & unix.S_IFMT
		if (kind != unix.S_IFDIR && kind != unix.S_IFREG) || (req.directory && kind != unix.S_IFDIR) || (!req.directory && !req.any && kind != unix.S_IFREG) {
			unix.Close(pinned)
			return 0, ErrUntrusted
		}
		proc, openErr := n.procFD()
		if openErr != nil {
			unix.Close(pinned)
			return 0, openErr
		}
		flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NONBLOCK
		if req.write {
			flags = unix.O_RDWR | unix.O_CLOEXEC | unix.O_NONBLOCK
		}
		if kind == unix.S_IFDIR {
			flags |= unix.O_DIRECTORY
		}
		fd, err = unix.Openat(proc, strconv.Itoa(pinned), flags, 0)
		unix.Close(proc)
		if err == nil {
			var after unix.Stat_t
			if check := unix.Fstat(fd, &after); check != nil {
				unix.Close(fd)
				fd = -1
				err = check
			} else if before.Dev != after.Dev || before.Ino != after.Ino {
				unix.Close(fd)
				fd = -1
				err = ErrChanged
			}
		}
		unix.Close(pinned)
	}
	if err != nil {
		return 0, linuxError(err)
	}
	h := unixHandle(fd)
	n.mu.Lock()
	n.locations[h] = linuxLocation{parent, name}
	n.mu.Unlock()
	return h, nil
}
func (n *linuxNative) inspect(h handle) (objectInfo, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(unixFD(h), &stat); err != nil {
		return objectInfo{}, err
	}
	info := objectInfo{id: fmt.Sprintf("%d:%d", stat.Dev, stat.Ino), owner: fmt.Sprintf("uid:%d", stat.Uid),
		change: fmt.Sprintf("%d:%d:%d:%d", stat.Mtim.Sec, stat.Mtim.Nsec, stat.Ctim.Sec, stat.Ctim.Nsec),
		links:  uint32(stat.Nlink), size: stat.Size, securityKnown: true, sticky: stat.Mode&unix.S_ISVTX != 0}
	switch stat.Mode & unix.S_IFMT {
	case unix.S_IFDIR:
		info.kind = kindDirectory
	case unix.S_IFREG:
		info.kind = kindRegular
	case unix.S_IFLNK:
		info.kind = kindLink
	default:
		info.kind = kindSpecial
	}
	for _, attribute := range []string{"system.posix_acl_access", "system.posix_acl_default"} {
		_, err := unix.Fgetxattr(unixFD(h), attribute, nil)
		if err == nil || (!errors.Is(err, unix.ENODATA) && !errors.Is(err, unix.ENOTSUP)) {
			info.securityKnown = false
		}
	}
	for _, entry := range []struct {
		mask      uint32
		principal string
	}{{0o070, "posix:group"}, {0o007, "posix:other"}} {
		permissions := uint32(stat.Mode) & entry.mask
		if entry.mask == 0o070 {
			permissions >>= 3
		}
		var rights accessRights
		if permissions&4 != 0 {
			rights |= rightRead
		}
		if permissions&2 != 0 {
			rights |= rightMutate
		}
		info.grants = append(info.grants, grant{principal: entry.principal, rights: rights})
	}
	return info, nil
}

func (n *linuxNative) verifyPrivateMetadata(h handle) error {
	var info unix.Stat_t
	if err := unix.Fstat(unixFD(h), &info); err != nil {
		return err
	}
	if info.Uid != 0 || info.Nlink != 1 || info.Mode&(unix.S_IFMT|07777) != unix.S_IFREG|0600 {
		return ErrUntrusted
	}
	return nil
}
func (n *linuxNative) read(h handle, b []byte) (int, error) {
	count, err := unix.Read(unixFD(h), b)
	if count == 0 && err == nil {
		err = io.EOF
	}
	return count, err
}
func (n *linuxNative) write(h handle, b []byte) (int, error) { return unix.Write(unixFD(h), b) }
func (n *linuxNative) flush(h handle) error                  { return unix.Fsync(unixFD(h)) }
func (n *linuxNative) path(h handle) (string, error) {
	proc, err := n.procFD()
	if err != nil {
		return "", err
	}
	defer unix.Close(proc)
	buffer := make([]byte, 1<<16)
	count, err := unix.Readlinkat(proc, strconv.Itoa(unixFD(h)), buffer)
	if err != nil {
		return "", err
	}
	if count == len(buffer) {
		return "", ErrLimit
	}
	path := string(buffer[:count])
	if !strings.HasPrefix(path, "/") || strings.HasSuffix(path, " (deleted)") {
		return "", ErrChanged
	}
	return path, nil
}
func (n *linuxNative) list(h handle) ([]string, error) {
	fd, err := unix.Openat(unixFD(h), ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "securefs-directory")
	defer file.Close()
	var names []string
	for {
		batch, err := file.Readdirnames(256)
		for _, name := range batch {
			if name != "." && name != ".." {
				if _, err := relativeParts(name); err != nil || strings.Contains(name, "/") {
					return nil, ErrUntrusted
				}
				names = append(names, name)
				if len(names) > 100000 {
					return nil, ErrLimit
				}
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
func (n *linuxNative) location(h handle) (linuxLocation, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	value, ok := n.locations[h]
	if !ok {
		return linuxLocation{}, ErrChanged
	}
	return value, nil
}
func (n *linuxNative) checkName(h, parent handle, name string) error {
	var original, current unix.Stat_t
	if err := unix.Fstat(unixFD(h), &original); err != nil {
		return err
	}
	if err := unix.Fstatat(unixFD(parent), name, &current, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return linuxError(err)
	}
	if original.Dev != current.Dev || original.Ino != current.Ino {
		return ErrChanged
	}
	return nil
}
func (n *linuxNative) rename(source, parent handle, name string, replace bool) error {
	location, err := n.location(source)
	if err != nil {
		return err
	}
	if err := n.checkName(source, location.parent, location.name); err != nil {
		return err
	}
	flags := uint(0)
	if !replace {
		flags = unix.RENAME_NOREPLACE
	}
	if err := unix.Renameat2(unixFD(location.parent), location.name, unixFD(parent), name, flags); err != nil {
		return linuxError(err)
	}
	n.mu.Lock()
	n.locations[source] = linuxLocation{parent, name}
	n.mu.Unlock()
	if err := unix.Fsync(unixFD(parent)); err != nil {
		return err
	}
	return nil
}
func (n *linuxNative) replacePrivate(parent, temp handle, temporary, target string) (CommitState, error) {
	if err := n.checkName(temp, parent, temporary); err != nil {
		return Unchanged, err
	}
	if err := unix.Renameat(unixFD(parent), temporary, unixFD(parent), target); err != nil {
		return Unchanged, linuxError(err)
	}
	n.mu.Lock()
	n.locations[temp] = linuxLocation{parent, target}
	n.mu.Unlock()
	if err := unix.Fsync(unixFD(parent)); err != nil {
		return Indeterminate, err
	}
	return Committed, nil
}
func (n *linuxNative) removeOwned(source, parent handle, name string) error {
	if err := n.checkName(source, parent, name); err != nil {
		return err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(unixFD(source), &stat); err != nil {
		return err
	}
	flags := 0
	if stat.Mode&unix.S_IFMT == unix.S_IFDIR {
		flags = unix.AT_REMOVEDIR
	}
	if err := unix.Unlinkat(unixFD(parent), name, flags); err != nil {
		return linuxError(err)
	}
	return unix.Fsync(unixFD(parent))
}
func (n *linuxNative) containQuarantined(h handle, policy Policy) error {
	location, err := n.location(h)
	if err != nil {
		return err
	}
	actor, _ := n.principal()
	return verifyForensicContainment(n, h, location.parent, policy, actor)
}
func (n *linuxNative) lock(h handle) error {
	return linuxError(unix.Flock(unixFD(h), unix.LOCK_EX|unix.LOCK_NB))
}
func (n *linuxNative) unlock(h handle) error { return linuxError(unix.Flock(unixFD(h), unix.LOCK_UN)) }
func (n *linuxNative) close(h handle) error {
	n.mu.Lock()
	delete(n.locations, h)
	n.mu.Unlock()
	return unix.Close(unixFD(h))
}
