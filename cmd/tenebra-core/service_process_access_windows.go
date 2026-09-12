//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"runtime"

	"golang.org/x/sys/windows"
)

// The GUI authenticates the pipe server using SCM, its process image, and a
// retained process handle. A LocalSystem process's inherited DACL need not let
// an ordinary console user query that image. Grant just that metadata right on
// OUR process before listening; never weaken the GUI's identity checks or grant
// process memory, duplication, termination, token, or security-editing rights.
// This is a process-lifetime ACL change, not a machine/service/token policy.
func enableServiceProcessQuery() error {
	identity, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return fmt.Errorf("read service identity: %w", err)
	}
	if identity.User.Sid == nil || !identity.User.Sid.IsWellKnown(windows.WinLocalSystemSid) {
		return errors.New("service must run as LocalSystem")
	}
	process, err := windows.OpenProcess(windows.READ_CONTROL|windows.WRITE_DAC, false, uint32(os.Getpid()))
	if err != nil {
		return fmt.Errorf("open own process security: %w", err)
	}
	defer windows.CloseHandle(process)
	original, err := windows.GetSecurityInfo(process, windows.SE_KERNEL_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("read own process DACL: %w", err)
	}
	updated, err := serviceProcessQueryACL(original)
	if err != nil {
		return err
	}
	// DACL only: preserve the owner, primary group, SACL/integrity label, and
	// protection flags. The merge preserves all existing grants and denials.
	if err := windows.SetSecurityInfo(process, windows.SE_KERNEL_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, updated, nil); err != nil {
		return fmt.Errorf("grant own process metadata query: %w", err)
	}
	actual, err := windows.GetSecurityInfo(process, windows.SE_KERNEL_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("read back own process DACL: %w", err)
	}
	if err := verifyServiceProcessQueryACL(updated, actual); err != nil {
		return err
	}
	before, _, err := original.Control()
	if err != nil {
		return fmt.Errorf("read original process DACL flags: %w", err)
	}
	after, _, err := actual.Control()
	if err != nil || before&windows.SE_DACL_PROTECTED != after&windows.SE_DACL_PROTECTED {
		return errors.New("own process DACL protection changed")
	}
	return nil
}

func serviceProcessQueryACL(original *windows.SECURITY_DESCRIPTOR) (*windows.ACL, error) {
	if original == nil || !original.IsValid() {
		return nil, errors.New("invalid own process security descriptor")
	}
	dacl, _, err := original.DACL()
	if err != nil || dacl == nil {
		return nil, errors.New("own process must have an explicit non-null DACL")
	}
	interactive, err := windows.CreateWellKnownSid(windows.WinInteractiveSid)
	if err != nil {
		return nil, fmt.Errorf("create interactive SID: %w", err)
	}
	entries := []windows.EXPLICIT_ACCESS{{
		AccessPermissions: windows.PROCESS_QUERY_LIMITED_INFORMATION,
		AccessMode:        windows.GRANT_ACCESS,
		Inheritance:       windows.NO_INHERITANCE,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_WELL_KNOWN_GROUP,
			TrusteeValue: windows.TrusteeValueFromSID(interactive),
		},
	}}
	merged, err := windows.ACLFromEntries(entries, dacl)
	runtime.KeepAlive(interactive)
	if err != nil {
		return nil, fmt.Errorf("merge own process metadata query ACL: %w", err)
	}
	return merged, nil
}

// Compare the entire DACL, not only the new ACE: a failed/partial readback must
// not publish a service as ready, nor conceal lost existing permissions.
func verifyServiceProcessQueryACL(expected *windows.ACL, actual *windows.SECURITY_DESCRIPTOR) error {
	if actual == nil || !actual.IsValid() {
		return errors.New("invalid own process security readback")
	}
	dacl, _, err := actual.DACL()
	if err != nil || dacl == nil || expected == nil {
		return errors.New("own process security readback has no explicit DACL")
	}
	want, err := processACLString(expected)
	if err != nil {
		return err
	}
	got, err := processACLString(dacl)
	if err != nil {
		return err
	}
	if want != got {
		return errors.New("own process metadata-query DACL readback differs")
	}
	return nil
}

func processACLString(dacl *windows.ACL) (string, error) {
	if dacl == nil {
		return "", errors.New("null process DACL")
	}
	sd, err := windows.NewSecurityDescriptor()
	if err != nil {
		return "", err
	}
	if err := sd.SetDACL(dacl, true, false); err != nil {
		return "", err
	}
	value := sd.String()
	runtime.KeepAlive(dacl)
	if value == "" {
		return "", errors.New("cannot encode process DACL")
	}
	return value, nil
}
