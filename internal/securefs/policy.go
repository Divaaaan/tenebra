package securefs

import (
	"errors"
	"path"
	"runtime"
	"strings"
	"unicode/utf8"
)

type Mode uint8

const (
	MachineContainer Mode = iota + 1
	MachinePrivate
	SameAccountPrivate
	OperatorInput
)

type Policy struct {
	Mode      Mode
	Principal string
}

var (
	ErrUntrusted   = errors.New("untrusted filesystem object")
	ErrChanged     = errors.New("filesystem object changed")
	ErrUnsupported = errors.New("unsupported filesystem operation")
	ErrBusy        = errors.New("filesystem object is busy")
	ErrClosed      = errors.New("filesystem object is closed")
	ErrLimit       = errors.New("filesystem resource limit exceeded")
)

func validPolicy(p Policy) bool {
	if p.Mode < MachineContainer || p.Mode > OperatorInput {
		return false
	}
	if p.Mode == OperatorInput {
		return p.Principal != ""
	}
	return p.Principal == ""
}

func trustedPrincipal(principal string, p Policy, actor string) bool {
	if principal == "uid:0" || principal == "S-1-5-18" || principal == "S-1-5-32-544" ||
		principal == "S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464" {
		return true
	}
	if p.Mode == SameAccountPrivate {
		return principal == actor
	}
	if p.Mode == OperatorInput {
		return principal == p.Principal
	}
	return false
}

func verifyObject(info objectInfo, policy Policy, actor string, container bool) error {
	if !info.securityKnown || !trustedPrincipal(info.owner, policy, actor) ||
		(info.kind != kindDirectory && info.kind != kindRegular) ||
		(info.kind == kindRegular && info.links != 1) {
		return ErrUntrusted
	}
	return verifyGrants(info, policy, actor, container)
}

// Object kinds remain checked by their own entry. The same ACL rules apply
// to a separately verified private IPC socket without admitting socket payloads
// to ordinary file, tree, staging or reader operations.
func verifyGrants(info objectInfo, policy Policy, actor string, container bool) error {
	for _, entry := range info.grants {
		if entry.unknown {
			return ErrUntrusted
		}
		if entry.inheritOnly || entry.deny || trustedPrincipal(entry.principal, policy, actor) {
			continue
		}
		if entry.rights&rightCreate != 0 && !container {
			return ErrUntrusted
		}
		if entry.rights&rightMutate != 0 {
			// A root-owned sticky namespace cannot unlink another owner's child.
			// Every opened child is still independently checked before any use.
			if !(container && info.sticky && info.owner == "uid:0") {
				return ErrUntrusted
			}
		}
		if !container && policy.Mode != OperatorInput && entry.rights&rightRead != 0 {
			return ErrUntrusted
		}
	}
	return nil
}

func relativeParts(name string) ([]string, error) {
	if name == "" || !utf8.ValidString(name) || strings.ContainsAny(name, "\\:\x00") || path.IsAbs(name) {
		return nil, ErrUntrusted
	}
	parts := strings.Split(name, "/")
	if len(parts) > 32 {
		return nil, ErrLimit
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return nil, ErrUntrusted
		}
		if runtime.GOOS == "windows" && !validWindowsComponent(part) {
			return nil, ErrUntrusted
		}
	}
	return parts, nil
}

func validWindowsComponent(name string) bool {
	if strings.TrimRight(name, ". ") != name || strings.ContainsAny(name, "<>\"|?*\r\n\t") {
		return false
	}
	stem := strings.ToUpper(strings.SplitN(name, ".", 2)[0])
	if stem == "CON" || stem == "PRN" || stem == "AUX" || stem == "NUL" || stem == "CONIN$" || stem == "CONOUT$" {
		return false
	}
	if strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT") {
		suffix := strings.TrimPrefix(strings.TrimPrefix(stem, "COM"), "LPT")
		runes := []rune(suffix)
		if len(runes) == 1 && strings.ContainsRune("123456789¹²³", runes[0]) {
			return false
		}
	}
	return true
}
