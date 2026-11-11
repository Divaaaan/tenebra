//go:build darwin

package securefs

import (
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"
)

// These fail at COMPILE time on each target architecture; a repeated portable
// fixture label is not evidence of either native ABI.
var _ [24 - unsafe.Sizeof(unix.Attrlist{})]byte
var _ [unsafe.Sizeof(unix.Attrlist{}) - 24]byte
var _ [4 - unsafe.Offsetof(unix.Attrlist{}.Commonattr)]byte
var _ [unsafe.Offsetof(unix.Attrlist{}.Commonattr) - 4]byte

func TestDarwinNativeAttrlistABI(t *testing.T) {
	if unix.SYS_FGETATTRLIST != 228 || unix.ATTR_CMN_EXTENDED_SECURITY != 0x00400000 || unix.ATTR_VOL_CAPABILITIES != 0x20000 {
		t.Fatal("native ABI changed; review the fd security wrapper before admission")
	}
}
