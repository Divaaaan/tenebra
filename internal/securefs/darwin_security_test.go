package securefs

import (
	"encoding/binary"
	"errors"
	"testing"
)

func darwinACLFixture(flags, rights uint32) []byte {
	data := make([]byte, 12+44+24)
	binary.LittleEndian.PutUint32(data, uint32(len(data)))
	binary.LittleEndian.PutUint32(data[4:], 8)
	binary.LittleEndian.PutUint32(data[8:], uint32(len(data)-12))
	binary.LittleEndian.PutUint32(data[12:], darwinFilesecMagic)
	binary.LittleEndian.PutUint32(data[48:], 1)
	copy(data[56:], []byte{0xab, 0xcd, 0xef, 0xab, 0xcd, 0xef, 0xab, 0xcd, 0xef, 0xab, 0xcd, 0xef, 0, 0, 0, 12})
	binary.LittleEndian.PutUint32(data[72:], flags)
	binary.LittleEndian.PutUint32(data[76:], rights)
	return data
}

func TestDarwinSecurityABIAndEffectiveGrants(t *testing.T) {
	// Portable byte fixtures exercise actual wire offsets. Native ABI
	// assertions are separately compiled in native_darwin_test on each target.
	facts, err := parseDarwinSecurity(darwinACLFixture(1, 1<<2), 0)
	if err != nil || len(facts.grants) != 1 || facts.grants[0].rights&rightMutate == 0 {
		t.Fatal(facts, err)
	}
	info := objectInfo{kind: kindDirectory, owner: "uid:0", links: 1, securityKnown: true, grants: facts.grants}
	if err := verifyObject(info, Policy{Mode: MachineContainer}, "uid:0", true); !errors.Is(err, ErrUntrusted) {
		t.Fatal("ACL write permission was ignored", err)
	}
	facts, err = parseDarwinSecurity(darwinACLFixture(2, 1<<4), 0)
	if err != nil {
		t.Fatal(err)
	}
	info.grants = facts.grants
	if err := verifyObject(info, Policy{Mode: MachinePrivate}, "uid:0", false); err != nil {
		t.Fatal("known everyone delete-deny refused", err)
	}
	owner := darwinACLFixture(1, 1<<21)
	binary.BigEndian.PutUint32(owner[68:72], 10)
	facts, err = parseDarwinSecurity(owner, 501)
	if err != nil || facts.grants[0].principal != "uid:501" {
		t.Fatal("owner grant was not fd-owner bound", facts, err)
	}
	info.owner, info.grants = "uid:501", facts.grants
	if err := verifyObject(info, Policy{Mode: SameAccountPrivate}, "uid:501", false); err != nil {
		t.Fatal("actual owner lost its private access", err)
	}
	if err := verifyObject(info, Policy{Mode: SameAccountPrivate}, "uid:502", false); !errors.Is(err, ErrUntrusted) {
		t.Fatal("different account inherited private authority", err)
	}
}

func TestDarwinPrivateACLReadAndInheritanceAreEvaluated(t *testing.T) {
	for _, right := range []uint32{1 << 1, 1 << 3, 1 << 24, 1 << 21} {
		facts, err := parseDarwinSecurity(darwinACLFixture(1, right), 0)
		if err != nil {
			t.Fatal(err)
		}
		info := objectInfo{kind: kindRegular, owner: "uid:0", links: 1, securityKnown: true, grants: facts.grants}
		if err := verifyObject(info, Policy{Mode: MachinePrivate}, "uid:0", false); !errors.Is(err, ErrUntrusted) {
			t.Fatal("private ACL exposure accepted", right, err)
		}
	}
	facts, err := parseDarwinSecurity(darwinACLFixture(1|1<<8, 1<<2), 0)
	if err != nil {
		t.Fatal(err)
	}
	info := objectInfo{kind: kindDirectory, owner: "uid:0", links: 1, securityKnown: true, grants: facts.grants}
	if err := verifyObject(info, Policy{Mode: MachineContainer}, "uid:0", true); err != nil {
		t.Fatal("inherit-only ACE changed current access", err)
	}
}

func TestDarwinSecurityRejectsAmbiguousABIAndUnknownAuthority(t *testing.T) {
	for name, mutate := range map[string]func([]byte){
		"length":               func(b []byte) { binary.LittleEndian.PutUint32(b, 1000) },
		"negative-offset":      func(b []byte) { binary.LittleEndian.PutUint32(b[4:], 0xfffffffc) },
		"offset-into-header":   func(b []byte) { binary.LittleEndian.PutUint32(b[4:], 0) },
		"offset-overflow":      func(b []byte) { binary.LittleEndian.PutUint32(b[4:], 0x7fffffff) },
		"payload-truncated":    func(b []byte) { binary.LittleEndian.PutUint32(b[8:], 4000) },
		"bad-magic":            func(b []byte) { b[12] ^= 1 },
		"unknown-owner-fields": func(b []byte) { b[16] = 1 },
		"entries-over-limit":   func(b []byte) { binary.LittleEndian.PutUint32(b[48:], 129) },
		"entries-inconsistent": func(b []byte) { binary.LittleEndian.PutUint32(b[48:], 0) },
		"deferred-inheritance": func(b []byte) { binary.LittleEndian.PutUint32(b[52:], 1<<16) },
		"unknown-acl-flag":     func(b []byte) { binary.LittleEndian.PutUint32(b[52:], 1) },
		"unknown-principal":    func(b []byte) { b[56] ^= 1 },
		"unknown-guid-code":    func(b []byte) { binary.BigEndian.PutUint32(b[68:], 42) },
		"audit-ace":            func(b []byte) { binary.LittleEndian.PutUint32(b[72:], 3) },
		"unknown-ace-flag":     func(b []byte) { binary.LittleEndian.PutUint32(b[72:], 1|1<<9) },
		"unknown-right":        func(b []byte) { binary.LittleEndian.PutUint32(b[76:], 1<<31) },
	} {
		t.Run(name, func(t *testing.T) {
			data := darwinACLFixture(1, 1<<1)
			mutate(data)
			if _, err := parseDarwinSecurity(data, 0); !errors.Is(err, ErrUntrusted) {
				t.Fatal("ambiguous ACL accepted", err)
			}
		})
	}
	for _, data := range [][]byte{nil, make([]byte, 11), make([]byte, darwinSecurityLimit+1)} {
		if _, err := parseDarwinSecurity(data, 0); err == nil {
			t.Fatal("invalid buffer accepted")
		}
	}
}

func TestDarwinSecurityRequiresVolumeCapabilityAndCanonicalEmptyReference(t *testing.T) {
	volume := make([]byte, 36)
	binary.LittleEndian.PutUint32(volume, 36)
	if darwinVolumeHasSecurity(volume) {
		t.Fatal("mode-only volume accepted")
	}
	binary.LittleEndian.PutUint32(volume[8:], darwinExtendedSecurityCapability)
	if darwinVolumeHasSecurity(volume) {
		t.Fatal("unvalidated capability accepted")
	}
	binary.LittleEndian.PutUint32(volume[24:], darwinExtendedSecurityCapability)
	if !darwinVolumeHasSecurity(volume) {
		t.Fatal("known valid capability refused")
	}
	for _, offset := range []uint32{0, 8} {
		empty := make([]byte, 12)
		binary.LittleEndian.PutUint32(empty, 12)
		binary.LittleEndian.PutUint32(empty[4:], offset)
		if _, err := parseDarwinSecurity(empty, 0); err != nil {
			t.Fatal("canonical absent ACL refused", err)
		}
	}
	empty := make([]byte, 12)
	binary.LittleEndian.PutUint32(empty, 12)
	binary.LittleEndian.PutUint32(empty[4:], 12)
	if _, err := parseDarwinSecurity(empty, 0); err == nil {
		t.Fatal("noncanonical empty reference accepted")
	}
}

func TestDarwinAliasesAreOnlyFixedOSPrefixes(t *testing.T) {
	for _, name := range []string{"var", "tmp", "etc"} {
		if !darwinSystemAlias(name, "private/"+name) || !darwinSystemAlias(name, "/private/"+name) {
			t.Fatal("known alias refused")
		}
	}
	for _, pair := range [][2]string{{"var", "private/var/evil"}, {"var", "/tmp"}, {"Library", "/private/Library"}, {"tmp", "../private/tmp"}, {"var", "private/../var"}} {
		if darwinSystemAlias(pair[0], pair[1]) {
			t.Fatal("arbitrary symlink followed", pair)
		}
	}
}
