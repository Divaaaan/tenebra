//go:build windows

package control

import (
	"golang.org/x/sys/windows"
	"testing"
	"unsafe"
)

// Parse the actual server descriptor with Windows' security descriptor parser.
// No pipe, registry key or service is opened by this test.
func TestInteractivePipeACEExcludesServerCreation(t *testing.T) {
	sd, err := windows.SecurityDescriptorFromString(pipeSecurityDescriptor)
	if err != nil {
		t.Fatal(err)
	}
	acl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if acl == nil {
		t.Fatal("pipe has unrestricted DACL")
	}
	iu, err := windows.StringToSid("S-1-5-4")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, i, &ace); err != nil {
			t.Fatal(err)
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			continue
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !windows.EqualSid(sid, iu) {
			continue
		}
		found = true
		const needed = windows.FILE_READ_DATA | windows.FILE_WRITE_DATA | windows.FILE_READ_ATTRIBUTES | windows.READ_CONTROL | windows.SYNCHRONIZE
		if ace.Mask != needed {
			t.Fatalf("interactive access = %#x, want exact client-only %#x", ace.Mask, needed)
		}
		const fileCreatePipeInstance = 0x4 // same bit as FILE_APPEND_DATA
		if ace.Mask&(fileCreatePipeInstance|windows.GENERIC_WRITE|windows.GENERIC_ALL|windows.WRITE_DAC|windows.WRITE_OWNER) != 0 {
			t.Fatalf("interactive user can create a server or rewrite pipe security: %#x", ace.Mask)
		}
	}
	if !found {
		t.Fatal("interactive client ACE missing")
	}
}
