//go:build windows

package control

import (
	"errors"
	"fmt"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const proxyLockDirectory = "Tenebra-private-proxy"

// The parent and child handles remain open throughout the operation. Children
// are opened relative to verified handles, so renaming any path ancestor cannot
// redirect a subsequent create. OBJ_DONT_REPARSE rejects junctions/symlinks.
// No global kernel object is exposed to precreation by another logged-in user.
func acquireUserProxyLock(sid string) (func(), error) {
	basePath, err := windows.KnownFolderPath(windows.FOLDERID_LocalAppData, 0)
	if err != nil {
		return nil, fmt.Errorf("resolve user proxy lock known folder: %w", err)
	}
	ntPath, err := proxyLockNTPath(basePath)
	if err != nil {
		return nil, err
	}
	var handles []windows.Handle
	release := func() {
		for i := len(handles) - 1; i >= 0; i-- {
			windows.CloseHandle(handles[i])
		}
		handles = nil
	}
	ok := false
	defer func() {
		if !ok {
			release()
		}
	}()
	base, err := openProxyLockNode(0, ntPath, true, false, nil)
	if err != nil {
		return nil, fmt.Errorf("open user proxy lock known folder without reparse: %w", err)
	}
	handles = append(handles, base)
	if err := checkProxyLockHandle(base, sid, true, false); err != nil {
		return nil, err
	}
	sd, err := windows.SecurityDescriptorFromString("O:" + sid + "D:P(A;;FA;;;SY)(A;;FA;;;BA)(A;;FA;;;" + sid + ")")
	if err != nil {
		return nil, err
	}
	dir, err := openProxyLockNode(base, proxyLockDirectory, true, true, sd)
	if err != nil {
		return nil, fmt.Errorf("open private user proxy lock directory: %w", err)
	}
	handles = append(handles, dir)
	if err := checkProxyLockHandle(dir, sid, true, true); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		lock, err := openProxyLockNode(dir, "operation.lock", false, true, sd)
		if err == nil {
			handles = append(handles, lock)
			if err := checkProxyLockHandle(lock, sid, false, true); err != nil {
				return nil, err
			}
			ok = true
			return release, nil
		}
		if !errors.Is(err, windows.STATUS_SHARING_VIOLATION) || !time.Now().Before(deadline) {
			return nil, fmt.Errorf("acquire private user proxy lock: %w", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func openProxyLockNode(parent windows.Handle, name string, directory, create bool, sd *windows.SECURITY_DESCRIPTOR) (windows.Handle, error) {
	objectName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return 0, err
	}
	oa := windows.OBJECT_ATTRIBUTES{
		RootDirectory: parent, ObjectName: objectName,
		Attributes:         windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE,
		SecurityDescriptor: sd,
	}
	oa.Length = uint32(unsafe.Sizeof(oa))
	access := uint32(windows.READ_CONTROL | windows.FILE_READ_ATTRIBUTES | windows.SYNCHRONIZE)
	options := uint32(windows.FILE_SYNCHRONOUS_IO_NONALERT | windows.FILE_NON_DIRECTORY_FILE)
	share := uint32(0) // exclusive file handle; released automatically after a crash
	if directory {
		access |= windows.FILE_TRAVERSE
		options = windows.FILE_SYNCHRONOUS_IO_NONALERT | windows.FILE_DIRECTORY_FILE
		share = windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE // never allow delete/rename
	} else {
		access |= windows.FILE_READ_DATA | windows.FILE_WRITE_DATA
	}
	disposition := uint32(windows.FILE_OPEN)
	if create {
		disposition = windows.FILE_OPEN_IF // never truncate an existing object
	}
	var handle windows.Handle
	var status windows.IO_STATUS_BLOCK
	err = windows.NtCreateFile(&handle, access, &oa, &status, nil, windows.FILE_ATTRIBUTE_NORMAL, share, disposition, options, 0, 0)
	return handle, err
}

func checkProxyLockHandle(handle windows.Handle, user string, directory, private bool) error {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return err
	}
	sd, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	owner, _, err := sd.Owner()
	if err != nil || owner == nil {
		return errors.New("user proxy lock owner unavailable")
	}
	acl, _, err := sd.DACL()
	if err != nil || acl == nil {
		return errors.New("user proxy lock DACL unavailable")
	}
	control, _, err := sd.Control()
	if err != nil {
		return err
	}
	node := proxyLockNode{
		Directory:     info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0,
		Reparse:       info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0,
		MultipleLinks: info.NumberOfLinks != 1, Owner: owner.String(),
		DACLPresent: true, Protected: control&windows.SE_DACL_PROTECTED != 0,
	}
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, i, &ace); err != nil {
			return err
		}
		if ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 || ace.Header.AceType == windows.ACCESS_DENIED_ACE_TYPE {
			continue // neither grants access to this object
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return errors.New("unsupported user proxy lock ACL entry")
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		node.Grants = append(node.Grants, proxyLockGrant{SID: sid.String(), Mask: uint32(ace.Mask)})
	}
	return validateProxyLockNode(node, user, directory, private)
}
