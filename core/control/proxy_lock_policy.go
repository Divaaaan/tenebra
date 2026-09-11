package control

import (
	"errors"
	"fmt"
	"strings"
)

// Only a local, unambiguous KnownFolder path is accepted. Do not derive this
// location from LOCALAPPDATA, TEMP, a working directory or a caller argument.
func proxyLockNTPath(path string) (string, error) {
	if len(path) < 4 || !((path[0] >= 'A' && path[0] <= 'Z') || (path[0] >= 'a' && path[0] <= 'z')) || path[1:3] != `:\` {
		return "", errors.New("user proxy lock requires an absolute local known folder")
	}
	for _, part := range strings.Split(path[3:], `\`) {
		if part == "" || part == "." || part == ".." || strings.TrimRight(part, ". ") != part || strings.ContainsAny(part, ":/\x00") {
			return "", errors.New("ambiguous user proxy lock path")
		}
		device := strings.ToUpper(strings.SplitN(part, ".", 2)[0])
		if device == "CON" || device == "PRN" || device == "AUX" || device == "NUL" || device == "CONIN$" || device == "CONOUT$" || (len(device) == 4 && (strings.HasPrefix(device, "COM") || strings.HasPrefix(device, "LPT")) && device[3] >= '1' && device[3] <= '9') {
			return "", errors.New("device name in user proxy lock path")
		}
	}
	return `\??\` + path, nil
}

type proxyLockGrant struct {
	SID  string
	Mask uint32
}

type proxyLockNode struct {
	Directory, Reparse, MultipleLinks bool
	Owner                             string
	DACLPresent, Protected            bool
	Grants                            []proxyLockGrant
}

func trustedProxyLockSID(sid, user string) bool {
	return sid != "" && (sid == user || sid == "S-1-5-18" || sid == "S-1-5-32-544")
}

// Same-user and administrator writes are already authorized to change that
// user's proxy. Everyone else must be unable to replace the directory or lock.
func validateProxyLockNode(n proxyLockNode, user string, directory, private bool) error {
	if user == "" || n.Directory != directory || n.Reparse || (!directory && n.MultipleLinks) {
		return errors.New("user proxy lock path has an unexpected file type or link")
	}
	if !trustedProxyLockSID(n.Owner, user) || !n.DACLPresent || (private && !n.Protected) {
		return errors.New("user proxy lock ownership or private DACL is unsafe")
	}
	// FILE_WRITE_DATA/APPEND_DATA/WRITE_EA/DELETE_CHILD/WRITE_ATTRIBUTES,
	// DELETE/WRITE_DAC/WRITE_OWNER, GENERIC_WRITE/ALL and MAXIMUM_ALLOWED.
	const mutation = 0x2 | 0x4 | 0x10 | 0x40 | 0x100 | 0x10000 | 0x40000 | 0x80000 | 0x40000000 | 0x10000000 | 0x02000000
	for _, grant := range n.Grants {
		if !trustedProxyLockSID(grant.SID, user) && ((private && grant.Mask != 0) || grant.Mask&mutation != 0) {
			return fmt.Errorf("user proxy lock permits access by another identity: %s", grant.SID)
		}
	}
	return nil
}
