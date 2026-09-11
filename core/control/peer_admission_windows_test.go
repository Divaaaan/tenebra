//go:build windows

package control

import (
	"encoding/binary"
	"errors"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestWindowsPeerAdmission(t *testing.T) {
	const self = "S-1-5-18"
	const console = "S-1-5-21-1-1001"
	const other = "S-1-5-21-1-1000"
	tests := []struct {
		name, peer, self, console string
		admin                     bool
		consoleErr                error
		want                      bool
	}{
		{"elevated installer under different admin", other, self, console, true, nil, true},
		{"elevated installer before logon", other, self, "", true, errors.New("no console"), true},
		{"elevated installer with failed self lookup", other, "", console, true, nil, true},
		{"filtered different admin", other, self, console, false, nil, false},
		{"ordinary console user", console, self, console, false, nil, true},
		{"ordinary unrelated user", other, self, console, false, nil, false},
		{"ordinary user with missing console", other, self, "", false, errors.New("no console"), false},
		{"daemon account before logon", self, self, "", false, errors.New("no console"), true},
		{"unknown SID despite admin claim", "", self, console, true, nil, false},
		{"empty identities", "", "", "", false, nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := windowsPeerAllowed(tt.peer, tt.self, tt.admin, func() (string, error) { return tt.console, tt.consoleErr }, func(string) {})
			if got != tt.want {
				t.Fatalf("channel admission = %v, want %v", got, tt.want)
			}
			if got && tt.peer == console && !tt.admin && peerPrivileged(tt.peer, tt.self, tt.admin) {
				t.Fatal("ordinary console user's channel must not grant privileged commands")
			}
		})
	}
}

func TestGroupEnabledRejectsContradictoryDenyOnly(t *testing.T) {
	admins, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		t.Fatal(err)
	}
	groups := []windows.SIDAndAttributes{{Sid: admins, Attributes: windows.SE_GROUP_ENABLED | windows.SE_GROUP_USE_FOR_DENY_ONLY}}
	if groupEnabled(groups, admins) {
		t.Fatal("deny-only membership must never grant authority, even with the enabled bit")
	}
}

// Each fixture represents complete results from the token-information boundary;
// the policy below is production code, with only the Windows query substituted.
func TestTokenFullAdminRights(t *testing.T) {
	tests := []struct {
		name                                                 string
		elevated, restricted, integrity                      uint32
		errorClass, shortClass                               uint32
		invalidSID, outsideBuffer, missingIntegrityAttribute bool
		want                                                 bool
	}{
		{name: "full elevated admin", elevated: 1, integrity: 0x3000, want: true},
		{name: "system integrity", elevated: 1, integrity: 0x4000, want: true},
		{name: "not elevated", integrity: 0x3000},
		{name: "restricted elevated token", elevated: 1, restricted: 1, integrity: 0x3000},
		{name: "low integrity elevated token", elevated: 1, integrity: 0x1000},
		{name: "medium integrity elevated token", elevated: 1, integrity: 0x2000},
		{name: "medium plus integrity elevated token", elevated: 1, integrity: 0x2100},
		{name: "elevation query error", elevated: 1, integrity: 0x3000, errorClass: windows.TokenElevation},
		{name: "restriction query error", elevated: 1, integrity: 0x3000, errorClass: windows.TokenHasRestrictions},
		{name: "integrity query error", elevated: 1, integrity: 0x3000, errorClass: windows.TokenIntegrityLevel},
		{name: "short elevation result", elevated: 1, integrity: 0x3000, shortClass: windows.TokenElevation},
		{name: "short restriction result", elevated: 1, integrity: 0x3000, shortClass: windows.TokenHasRestrictions},
		{name: "short integrity result", elevated: 1, integrity: 0x3000, shortClass: windows.TokenIntegrityLevel},
		{name: "invalid integrity authority", elevated: 1, integrity: 0x3000, invalidSID: true},
		{name: "integrity SID outside returned buffer", elevated: 1, integrity: 0x3000, outsideBuffer: true},
		{name: "missing integrity attribute", elevated: 1, integrity: 0x3000, missingIntegrityAttribute: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			query := func(_ windows.Token, class uint32, info *byte, size uint32, out *uint32) error {
				if class == tt.errorClass {
					return windows.ERROR_ACCESS_DENIED
				}
				buffer := unsafe.Slice(info, size)
				switch class {
				case windows.TokenElevation:
					binary.LittleEndian.PutUint32(buffer, tt.elevated)
					*out = 4
				case windows.TokenHasRestrictions:
					binary.LittleEndian.PutUint32(buffer, tt.restricted)
					*out = 4
				case windows.TokenIntegrityLevel:
					header := int(unsafe.Sizeof(windows.Tokenmandatorylabel{}))
					if len(buffer) < header+12 {
						return windows.ERROR_INSUFFICIENT_BUFFER
					}
					label := (*windows.Tokenmandatorylabel)(unsafe.Pointer(info))
					label.Label.Attributes = windows.SE_GROUP_INTEGRITY
					if tt.missingIntegrityAttribute {
						label.Label.Attributes = 0
					}
					label.Label.Sid = (*windows.SID)(unsafe.Add(unsafe.Pointer(info), header))
					sid := buffer[header : header+12]
					copy(sid, []byte{1, 1, 0, 0, 0, 0, 0, 16, 0, 0, 0, 0})
					binary.LittleEndian.PutUint32(sid[8:], tt.integrity)
					if tt.invalidSID {
						sid[7] = 5
					}
					*out = uint32(header + 12)
					if tt.outsideBuffer {
						label.Label.Sid = (*windows.SID)(unsafe.Add(unsafe.Pointer(info), *out))
					}
				default:
					return windows.ERROR_INVALID_PARAMETER
				}
				if class == tt.shortClass {
					*out = 1
				}
				return nil
			}
			if got := tokenHasFullAdminRights(0, query); got != tt.want {
				t.Fatalf("full admin rights = %v, want %v", got, tt.want)
			}
		})
	}
}
