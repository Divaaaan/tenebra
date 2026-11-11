//go:build windows

package securefs

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"strings"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

type windowsLocation struct {
	parent handle
	name   string
}
type windowsNative struct {
	actor     string
	mu        sync.Mutex
	locations map[handle]windowsLocation
}

// FILE_DELETE_CHILD from the Windows SDK is not exported by x/sys/windows.
const winFileDeleteChild = 0x0040

func newNativeOps() (nativeOps, error) {
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return nil, ErrUnsupported
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		return nil, ErrUnsupported
	}
	return &windowsNative{actor: user.User.Sid.String(), locations: map[handle]windowsLocation{}}, nil
}
func (n *windowsNative) principal() (string, error) { return n.actor, nil }

func winError(err error) error {
	if err == nil {
		return nil
	}
	if status, ok := err.(windows.NTStatus); ok {
		err = status.Errno()
	}
	if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_PATH_NOT_FOUND) {
		return fs.ErrNotExist
	}
	if errors.Is(err, windows.ERROR_CANT_ACCESS_FILE) || errors.Is(err, windows.ERROR_REPARSE_TAG_INVALID) || errors.Is(err, windows.ERROR_REPARSE_TAG_MISMATCH) {
		return ErrUntrusted
	}
	if errors.Is(err, windows.ERROR_SHARING_VIOLATION) || errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return ErrBusy
	}
	return err
}

func (n *windowsNative) openVolume(path string) (handle, error) {
	if strings.HasPrefix(path, `\\?\`) {
		path = strings.TrimPrefix(path, `\\?\`)
	}
	if len(path) != 3 || path[1] != ':' || (path[2] != '\\' && path[2] != '/') {
		return 0, ErrUnsupported
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, ErrUntrusted
	}
	h, err := windows.CreateFile(name, windows.READ_CONTROL|windows.FILE_READ_ATTRIBUTES|windows.FILE_LIST_DIRECTORY|windows.FILE_TRAVERSE|windows.SYNCHRONIZE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	return handle(h), winError(err)
}

func (n *windowsNative) descriptor(policy Policy) (*windows.SECURITY_DESCRIPTOR, error) {
	owner := "BA"
	if n.actor == "S-1-5-18" {
		owner = "SY"
	}
	if policy.Mode == SameAccountPrivate {
		owner = n.actor
	}
	if policy.Mode == OperatorInput {
		return nil, ErrUnsupported
	}
	sddl := "O:" + owner + "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)"
	if policy.Mode == SameAccountPrivate && n.actor != "S-1-5-18" && n.actor != "S-1-5-32-544" {
		sddl += "(A;OICI;FA;;;" + n.actor + ")"
	}
	return windows.SecurityDescriptorFromString(sddl)
}

func (n *windowsNative) openAt(parent handle, name string, req openRequest) (handle, error) {
	if _, err := relativeParts(name); err != nil || strings.Contains(name, "/") {
		return 0, ErrUntrusted
	}
	unicode, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return 0, ErrUntrusted
	}
	attributes := windows.OBJECT_ATTRIBUTES{RootDirectory: windows.Handle(parent), ObjectName: unicode,
		Attributes: windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE}
	attributes.Length = uint32(unsafe.Sizeof(attributes))
	access := uint32(windows.READ_CONTROL | windows.FILE_READ_ATTRIBUTES | windows.SYNCHRONIZE)
	options := uint32(windows.FILE_SYNCHRONOUS_IO_NONALERT | windows.FILE_OPEN_REPARSE_POINT)
	if req.directory {
		options |= windows.FILE_DIRECTORY_FILE
		access |= windows.FILE_LIST_DIRECTORY | windows.FILE_TRAVERSE
	} else if req.any {
		access |= windows.FILE_READ_DATA | windows.FILE_TRAVERSE
	} else {
		options |= windows.FILE_NON_DIRECTORY_FILE
		access |= windows.FILE_READ_DATA
	}
	if req.write {
		access |= windows.FILE_WRITE_DATA | windows.DELETE
	}
	if req.delete {
		access |= windows.DELETE
	}
	share := uint32(windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE | windows.FILE_SHARE_DELETE)
	if req.freeze {
		share = windows.FILE_SHARE_READ
	}
	disposition := uint32(windows.FILE_OPEN)
	if req.create {
		disposition = windows.FILE_CREATE
		attributes.SecurityDescriptor, err = n.descriptor(req.policy)
		if err != nil {
			return 0, err
		}
	}
	var h windows.Handle
	var result windows.IO_STATUS_BLOCK
	err = windows.NtCreateFile(&h, access, &attributes, &result, nil, windows.FILE_ATTRIBUTE_NORMAL, share, disposition, options, 0, 0)
	if err == nil {
		n.mu.Lock()
		n.locations[handle(h)] = windowsLocation{parent, name}
		n.mu.Unlock()
	}
	return handle(h), winError(err)
}

type winBasicInfo struct {
	CreationTime, LastAccessTime, LastWriteTime, ChangeTime int64
	Attributes                                              uint32
}

func (n *windowsNative) inspect(h handle) (objectInfo, error) {
	fileType, err := windows.GetFileType(windows.Handle(h))
	if err != nil {
		return objectInfo{}, winError(err)
	}
	if fileType != windows.FILE_TYPE_DISK {
		return objectInfo{kind: kindSpecial}, nil
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(h), &info); err != nil {
		return objectInfo{}, winError(err)
	}
	var basic winBasicInfo
	if err := windows.GetFileInformationByHandleEx(windows.Handle(h), windows.FileBasicInfo, (*byte)(unsafe.Pointer(&basic)), uint32(unsafe.Sizeof(basic))); err != nil {
		return objectInfo{}, winError(err)
	}
	result := objectInfo{id: fmt.Sprintf("%08x:%08x%08x", info.VolumeSerialNumber, info.FileIndexHigh, info.FileIndexLow),
		change: fmt.Sprintf("%d:%d", basic.LastWriteTime, basic.ChangeTime), size: int64(uint64(info.FileSizeHigh)<<32 | uint64(info.FileSizeLow)), links: info.NumberOfLinks, kind: kindRegular}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
		result.kind = kindDirectory
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		result.kind = kindLink
	}
	sd, err := windows.GetSecurityInfo(windows.Handle(h), windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return objectInfo{}, winError(err)
	}
	owner, _, err := sd.Owner()
	if err != nil || owner == nil {
		return objectInfo{}, ErrUntrusted
	}
	result.owner = owner.String()
	control, _, err := sd.Control()
	if err != nil || control&windows.SE_DACL_PRESENT == 0 {
		return objectInfo{}, ErrUntrusted
	}
	acl, _, err := sd.DACL()
	if err != nil || acl == nil {
		return objectInfo{}, ErrUntrusted
	}
	if acl.AceCount > 4096 {
		return objectInfo{}, ErrLimit
	}
	for index := uint32(0); index < uint32(acl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, index, &ace); err != nil || ace == nil {
			return objectInfo{}, ErrUntrusted
		}
		entry := grant{inheritOnly: ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE && ace.Header.AceType != windows.ACCESS_DENIED_ACE_TYPE {
			entry.unknown = true
			result.grants = append(result.grants, entry)
			continue
		}
		if uintptr(ace.Header.AceSize) < unsafe.Offsetof(ace.SidStart)+8 {
			return objectInfo{}, ErrUntrusted
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !sid.IsValid() || uintptr(sid.Len()) > uintptr(ace.Header.AceSize)-unsafe.Offsetof(ace.SidStart) {
			return objectInfo{}, ErrUntrusted
		}
		entry.principal = sid.String()
		entry.deny = ace.Header.AceType == windows.ACCESS_DENIED_ACE_TYPE
		if ace.Mask&(windows.GENERIC_READ|windows.GENERIC_ALL|windows.FILE_READ_DATA|windows.FILE_READ_EA|windows.FILE_READ_ATTRIBUTES) != 0 {
			entry.rights |= rightRead
		}
		if ace.Mask&(windows.GENERIC_WRITE|windows.GENERIC_ALL|windows.DELETE|windows.WRITE_DAC|windows.WRITE_OWNER|windows.FILE_WRITE_EA|windows.FILE_WRITE_ATTRIBUTES|winFileDeleteChild) != 0 {
			entry.rights |= rightMutate
		}
		if ace.Mask&(windows.FILE_WRITE_DATA|windows.FILE_APPEND_DATA) != 0 {
			if result.kind == kindDirectory {
				entry.rights |= rightCreate
			} else {
				entry.rights |= rightMutate
			}
		}
		result.grants = append(result.grants, entry)
	}
	result.securityKnown = true
	return result, nil
}

func (n *windowsNative) read(h handle, b []byte) (int, error) {
	var count uint32
	err := windows.ReadFile(windows.Handle(h), b, &count, nil)
	if err == nil && count == 0 {
		err = io.EOF
	}
	return int(count), winError(err)
}
func (n *windowsNative) write(h handle, b []byte) (int, error) {
	var count uint32
	err := windows.WriteFile(windows.Handle(h), b, &count, nil)
	return int(count), winError(err)
}
func (n *windowsNative) flush(h handle) error {
	return winError(windows.FlushFileBuffers(windows.Handle(h)))
}
func (n *windowsNative) close(h handle) error {
	n.mu.Lock()
	delete(n.locations, h)
	n.mu.Unlock()
	return winError(windows.CloseHandle(windows.Handle(h)))
}

func (n *windowsNative) path(h handle) (string, error) {
	buffer := make([]uint16, 32768)
	count, err := windows.GetFinalPathNameByHandle(windows.Handle(h), &buffer[0], uint32(len(buffer)), 0)
	if err != nil {
		return "", winError(err)
	}
	if count == 0 || count >= uint32(len(buffer)) {
		return "", ErrLimit
	}
	return windows.UTF16ToString(buffer[:count]), nil
}

type winDirInfo struct {
	NextEntryOffset, FileIndex                                                         uint32
	CreationTime, LastAccessTime, LastWriteTime, ChangeTime, EndOfFile, AllocationSize int64
	Attributes, FileNameLength, EaSize                                                 uint32
	ShortNameLength                                                                    uint8
	ShortName                                                                          [12]uint16
	FileID                                                                             int64
	FileName                                                                           [1]uint16
}

func (n *windowsNative) list(h handle) ([]string, error) {
	buffer := make([]byte, 64<<10)
	class := uint32(windows.FileIdBothDirectoryRestartInfo)
	var names []string
	seen := map[string]bool{}
	for {
		err := windows.GetFileInformationByHandleEx(windows.Handle(h), class, &buffer[0], uint32(len(buffer)))
		if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
			break
		}
		if err != nil {
			return nil, winError(err)
		}
		class = windows.FileIdBothDirectoryInfo
		for offset := uintptr(0); ; {
			const maxEntries = 100000
			if len(names) >= maxEntries {
				return nil, ErrLimit
			}
			fixed := unsafe.Offsetof(winDirInfo{}.FileName)
			if offset+fixed > uintptr(len(buffer)) {
				return nil, ErrChanged
			}
			info := (*winDirInfo)(unsafe.Pointer(&buffer[offset]))
			length := uintptr(info.FileNameLength)
			if length%2 != 0 || length > 32766 || offset+fixed+length > uintptr(len(buffer)) {
				return nil, ErrChanged
			}
			name := windows.UTF16ToString(unsafe.Slice((*uint16)(unsafe.Pointer(&buffer[offset+fixed])), length/2))
			if name != "." && name != ".." {
				if _, err := relativeParts(name); err != nil || strings.Contains(name, "/") {
					return nil, ErrUntrusted
				}
				key := strings.ToUpper(name)
				if seen[key] {
					return nil, ErrUntrusted
				}
				seen[key] = true
				names = append(names, name)
			}
			if info.NextEntryOffset == 0 {
				break
			}
			next := uintptr(info.NextEntryOffset)
			if next < fixed+length || offset+next >= uintptr(len(buffer)) {
				return nil, ErrChanged
			}
			offset += next
		}
	}
	return names, nil
}

type winRenameInfo struct {
	Replace uint8
	Root    windows.Handle
	Length  uint32
	Name    [1]uint16
}

func (n *windowsNative) rename(source, parent handle, name string, replace bool) error {
	if _, err := relativeParts(name); err != nil || strings.Contains(name, "/") {
		return ErrUntrusted
	}
	encoded, err := windows.UTF16FromString(name)
	if err != nil {
		return ErrUntrusted
	}
	encoded = encoded[:len(encoded)-1]
	header := unsafe.Offsetof(winRenameInfo{}.Name)
	buffer := make([]byte, header+uintptr(len(encoded)*2))
	info := (*winRenameInfo)(unsafe.Pointer(&buffer[0]))
	if replace {
		info.Replace = 1
	}
	info.Root = windows.Handle(parent)
	info.Length = uint32(len(encoded) * 2)
	copy(unsafe.Slice((*uint16)(unsafe.Pointer(&buffer[header])), len(encoded)), encoded)
	var result windows.IO_STATUS_BLOCK
	err = winError(windows.NtSetInformationFile(windows.Handle(source), &result, &buffer[0], uint32(len(buffer)), windows.FileRenameInformation))
	if err == nil {
		n.mu.Lock()
		n.locations[source] = windowsLocation{parent, name}
		n.mu.Unlock()
	}
	return err
}

func (n *windowsNative) replacePrivate(parent, temp handle, temporary, target string) (CommitState, error) {
	directory, err := n.path(parent)
	if err != nil {
		return Unchanged, err
	}
	source, err := windows.UTF16PtrFromString(filepath.Join(directory, temporary))
	if err != nil {
		return Unchanged, err
	}
	destination, err := windows.UTF16PtrFromString(filepath.Join(directory, target))
	if err != nil {
		return Unchanged, err
	}
	err = windows.MoveFileEx(source, destination, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
	if err == nil {
		return Committed, nil
	}
	// A still-named source with the same kernel identity confirms no rename.
	before, inspectErr := n.inspect(temp)
	check, openErr := n.openAt(parent, temporary, openRequest{})
	if inspectErr == nil && openErr == nil {
		after, checkErr := n.inspect(check)
		n.close(check)
		if checkErr == nil && before.id == after.id {
			return Unchanged, winError(err)
		}
	}
	return Indeterminate, winError(err)
}

func (n *windowsNative) removeOwned(source, parent handle, name string) error {
	info, err := n.inspect(source)
	if err != nil {
		return err
	}
	entry, err := n.openAt(parent, name, openRequest{directory: info.kind == kindDirectory, delete: true})
	if err != nil {
		return err
	}
	defer n.close(entry)
	current, err := n.inspect(entry)
	if err != nil {
		return err
	}
	if current.id != info.id {
		return ErrChanged
	}
	var result windows.IO_STATUS_BLOCK
	remove := byte(1)
	return winError(windows.NtSetInformationFile(windows.Handle(entry), &result, &remove, 1, windows.FileDispositionInformation))
}

func (n *windowsNative) containQuarantined(h handle, p Policy) error {
	n.mu.Lock()
	location, ok := n.locations[h]
	n.mu.Unlock()
	if !ok {
		return ErrUnsupported
	}
	return verifyForensicContainment(n, h, location.parent, p, n.actor)
}
func (n *windowsNative) lock(h handle) error {
	var overlapped windows.Overlapped
	return winError(windows.LockFileEx(windows.Handle(h), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &overlapped))
}
func (n *windowsNative) unlock(h handle) error {
	var overlapped windows.Overlapped
	return winError(windows.UnlockFileEx(windows.Handle(h), 0, 1, 0, &overlapped))
}
