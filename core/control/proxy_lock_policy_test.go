package control

import (
	"fmt"
	"testing"
)

func TestProxyLockPathRejectsAmbiguousAndRemoteLocations(t *testing.T) {
	for _, path := range []string{"", `C:relative`, `\\server\share\Local`, `\\?\C:\Local`, `C:\Users\u\..\other`, `C:\Users\u\Local.`, `C:\Users\u\Local `, `C:\Users\u\Local:stream`, `C:\Users\NUL\Local`, `C:\Users\u\\Local`, "C:\\Users\\u\\Local\x00"} {
		if _, err := proxyLockNTPath(path); err == nil {
			t.Errorf("accepted unsafe known folder %q", path)
		}
	}
	got, err := proxyLockNTPath(`C:\Users\Даня\AppData\Local`)
	if err != nil || got != `\??\C:\Users\Даня\AppData\Local` {
		t.Fatalf("valid Unicode known folder failed: %q %v", got, err)
	}
}

func TestProxyLockPolicyRejectsOtherUsersAndReparsePoints(t *testing.T) {
	const user = "S-1-5-21-1000"
	good := proxyLockNode{Directory: true, Owner: user, DACLPresent: true, Protected: true,
		Grants: []proxyLockGrant{{SID: user, Mask: 0x1f01ff}, {SID: "S-1-5-18", Mask: 0x1f01ff}, {SID: "S-1-5-32-544", Mask: 0x1f01ff}}}
	if err := validateProxyLockNode(good, user, true, true); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		change func(*proxyLockNode)
	}{
		{"foreign owner", func(n *proxyLockNode) { n.Owner = "S-1-5-21-2000" }},
		{"null DACL", func(n *proxyLockNode) { n.DACLPresent = false }},
		{"inheritable private DACL", func(n *proxyLockNode) { n.Protected = false }},
		{"junction", func(n *proxyLockNode) { n.Reparse = true }},
		{"file instead of directory", func(n *proxyLockNode) { n.Directory = false }},
		{"foreign read grant", func(n *proxyLockNode) { n.Grants = append(n.Grants, proxyLockGrant{SID: "S-1-1-0", Mask: 0x80000000}) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			n := good
			test.change(&n)
			if validateProxyLockNode(n, user, true, true) == nil {
				t.Fatal("unsafe private lock location accepted")
			}
		})
	}
	file := good
	file.Directory = false
	if err := validateProxyLockNode(file, user, false, true); err != nil {
		t.Fatal(err)
	}
	file.MultipleLinks = true
	if validateProxyLockNode(file, user, false, true) == nil {
		t.Fatal("hard-linked lock file accepted")
	}
}

func TestProxyLockKnownFolderPermitsReadOnlyButNotForeignMutation(t *testing.T) {
	const user = "S-1-5-21-1000"
	n := proxyLockNode{Directory: true, Owner: user, DACLPresent: true,
		Grants: []proxyLockGrant{{SID: user, Mask: 0x1f01ff}, {SID: "S-1-5-32-545", Mask: 0x1200a9}}}
	if err := validateProxyLockNode(n, user, true, false); err != nil {
		t.Fatal(err)
	}
	for _, mask := range []uint32{0x2, 0x4, 0x10, 0x40, 0x100, 0x10000, 0x40000, 0x80000, 0x40000000, 0x10000000, 0x02000000} {
		t.Run(fmt.Sprintf("%#x", mask), func(t *testing.T) {
			bad := n
			bad.Grants = append(bad.Grants, proxyLockGrant{SID: "S-1-5-21-2000", Mask: mask})
			if validateProxyLockNode(bad, user, true, false) == nil {
				t.Errorf("foreign mutation mask %#x accepted", mask)
			}
		})
	}
}
