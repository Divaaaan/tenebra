//go:build windows && (amd64 || arm64)

package protection

import (
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

func (b *windowsBackend) identities() (map[string]appIdentity, error) {
	if b.enginePath == nil {
		return nil, errors.New("engine executable identity unavailable")
	}
	engine, err := b.enginePath()
	if err != nil {
		return nil, err
	}
	core, err := os.Executable()
	if err != nil {
		return nil, err
	}
	systemDir, err := windows.GetSystemDirectory()
	if err != nil {
		return nil, err
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	dhcpSID, _, _, err := windows.LookupSID("", "NT SERVICE\\Dhcp")
	if err != nil {
		return nil, err
	}
	paths := map[string]string{Core: core, Engine: engine, DHCP: filepath.Join(systemDir, "svchost.exe")}
	result := make(map[string]appIdentity)
	for name, path := range paths {
		if err := trustedExecutable(path); err != nil {
			return nil, fmt.Errorf("%s protection identity: %w", name, err)
		}
		sid := user.User.Sid.String()
		if name == DHCP {
			sid = dhcpSID.String()
		}
		// FWP_ACTRL_MATCH_FILTER=1. Match the account/service token as well as
		// path; a same-name binary run by another account gets no exemption.
		sd, err := windows.SecurityDescriptorFromString("D:(A;;CC;;;" + sid + ")")
		if err != nil {
			return nil, err
		}
		path16, err := windows.UTF16PtrFromString(path)
		if err != nil {
			return nil, err
		}
		var blob *byteBlob
		if err := b.call("FwpmGetAppIdFromFileName0", ptr(path16), ptr(&blob)); err != nil {
			return nil, err
		}
		if blob == nil || blob.Data == nil || blob.Size == 0 || blob.Size > 65536 {
			if blob != nil {
				b.free(unsafe.Pointer(blob))
			}
			return nil, errors.New("invalid WFP executable identity")
		}
		app := append([]byte(nil), unsafe.Slice(blob.Data, blob.Size)...)
		b.free(unsafe.Pointer(blob))
		sdBytes := append([]byte(nil), unsafe.Slice((*byte)(unsafe.Pointer(sd)), sd.Length())...)
		runtime.KeepAlive(sd)
		result[name] = appIdentity{app, sdBytes}
	}
	return result, nil
}

// trustedExecutable is deliberately conservative. A persistent unrestricted
// application permit must never point at an ordinary user's replaceable file.
// ACL checks include owner-implied WRITE_DAC, parent DELETE_CHILD and reparse
// points. This rejects portable/user-writable installs with an actionable error.
func trustedExecutable(path string) error {
	if !filepath.IsAbs(path) || strings.HasPrefix(path, `\\`) {
		return errors.New("protection requires an absolute local executable path")
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return errors.New("executable path is a directory")
	}
	trustedInstaller, _, _, err := windows.LookupSID("", "NT SERVICE\\TrustedInstaller")
	if err != nil {
		return err
	}
	trusted := func(s *windows.SID) bool {
		return s != nil && (s.String() == "S-1-5-18" || s.String() == "S-1-5-32-544" || s.String() == trustedInstaller.String())
	}
	for depth, current := 0, filepath.Clean(path); ; depth, current = depth+1, filepath.Dir(current) {
		name, err := windows.UTF16PtrFromString(current)
		if err != nil {
			return err
		}
		attrs, err := windows.GetFileAttributes(name)
		if err != nil {
			return err
		}
		if attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
			return fmt.Errorf("protected executable path traverses a reparse point: %s", current)
		}
		sd, err := windows.GetNamedSecurityInfo(current, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
		if err != nil {
			return err
		}
		owner, _, err := sd.Owner()
		if err != nil {
			return err
		}
		if !trusted(owner) {
			return fmt.Errorf("install Tenebra in an administrator-owned directory: %s", current)
		}
		acl, _, err := sd.DACL()
		if err != nil {
			return err
		}
		if acl == nil {
			return fmt.Errorf("unrestricted executable ACL: %s", current)
		}
		// File/containing directory mutations and ancestor replacement rights.
		var dangerous windows.ACCESS_MASK = 0x10000000 | 0x40000000 | 0x00010000 | 0x00040000 | 0x00080000 | 0x40
		if depth <= 1 {
			dangerous |= 0x2 | 0x4 | 0x10 | 0x100
		}
		for i := uint32(0); i < uint32(acl.AceCount); i++ {
			var ace *windows.ACCESS_ALLOWED_ACE
			if err := windows.GetAce(acl, i, &ace); err != nil {
				return err
			}
			if ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 || ace.Header.AceType == windows.ACCESS_DENIED_ACE_TYPE {
				continue
			}
			if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
				return fmt.Errorf("unsupported executable ACL entry: %s", current)
			}
			sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
			if ace.Mask&dangerous != 0 && !trusted(sid) {
				return fmt.Errorf("executable can be replaced by an untrusted identity: %s", current)
			}
		}
		runtime.KeepAlive(sd)
		if parent := filepath.Dir(current); parent == current {
			break
		}
		if depth >= 32 {
			return errors.New("executable path exceeds trust-check depth")
		}
	}
	return nil
}

func (b *windowsBackend) ResolveTunnel(name, address string) (uint64, error) {
	prefix, err := netip.ParsePrefix(address)
	if err != nil || name == "" {
		return 0, errors.New("TUN identity requires its configured name and address")
	}
	var size uint32 = 16384
	for attempt := 0; attempt < 4; attempt++ {
		if size == 0 || size > 4<<20 {
			return 0, errors.New("adapter enumeration exceeded bound")
		}
		buf := make([]byte, size)
		first := (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0]))
		err := windows.GetAdaptersAddresses(windows.AF_UNSPEC, windows.GAA_FLAG_SKIP_ANYCAST|windows.GAA_FLAG_SKIP_MULTICAST|windows.GAA_FLAG_SKIP_DNS_SERVER, 0, first, &size)
		if err == windows.ERROR_BUFFER_OVERFLOW {
			continue
		}
		if err != nil {
			return 0, err
		}
		var found uint64
		for a := first; a != nil; a = a.Next {
			if windows.UTF16PtrToString(a.FriendlyName) != name {
				continue
			}
			if found != 0 || a.Luid == 0 || (a.IfType != 53 && a.IfType != 131) || a.OperStatus != windows.IfOperStatusUp {
				return 0, errors.New("configured TUN is not a unique active virtual interface")
			}
			matched := false
			for u := a.FirstUnicastAddress; u != nil; u = u.Next {
				ip, ok := netip.AddrFromSlice(u.Address.IP())
				if ok && ip.Unmap() == prefix.Addr().Unmap() {
					matched = true
				}
			}
			if !matched {
				return 0, errors.New("configured TUN does not own its expected address")
			}
			found = a.Luid
		}
		runtime.KeepAlive(buf)
		if found != 0 {
			return found, nil
		}
		return 0, errors.New("configured TUN was not found")
	}
	return 0, errors.New("adapter list kept changing")
}
