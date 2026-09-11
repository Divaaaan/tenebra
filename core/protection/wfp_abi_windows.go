//go:build windows && (amd64 || arm64)

package protection

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"runtime"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// These are the 64-bit SDK layouts, including FWPM_FILTER0's 16-byte UNION.
// https://learn.microsoft.com/windows/win32/api/fwpmtypes/ns-fwpmtypes-fwpm_filter0
type displayData struct{ Name, Description *uint16 }
type byteBlob struct {
	Size uint32
	Data *byte
}
type wfpValue struct {
	Type  uint32
	Value uintptr
}

// pointer reads the pointer member of the SDK's value union without rebuilding
// a pointer from an integer. Call only for pointer-valued FWP data types; inline
// UINT8/16/32 remain numbers so Go's GC never scans them as pointers.
func (v *wfpValue) pointer() unsafe.Pointer {
	return *(*unsafe.Pointer)(unsafe.Pointer(&v.Value))
}

type wfpSession struct {
	Key                 windows.GUID
	Display             displayData
	Flags, Timeout, PID uint32
	SID                 *windows.SID
	Username            *uint16
	KernelMode          uint8
}
type wfpProvider struct {
	Key         windows.GUID
	Display     displayData
	Flags       uint32
	Data        byteBlob
	ServiceName *uint16
}
type wfpSublayer struct {
	Key      windows.GUID
	Display  displayData
	Flags    uint32
	Provider *windows.GUID
	Data     byteBlob
	Weight   uint16
}
type wfpAction struct {
	Type uint32
	Key  windows.GUID
}
type wfpCondition struct {
	Field windows.GUID
	Match uint32
	Value wfpValue
}
type wfpFilter struct {
	Key             windows.GUID
	Display         displayData
	Flags           uint32
	Provider        *windows.GUID
	Data            byteBlob
	Layer, Sublayer windows.GUID
	Weight          wfpValue
	Count           uint32
	Conditions      *wfpCondition
	Action          wfpAction
	Context         [2]uint64 // rawContext OR providerContextKey, never two sequential fields
	Reserved        *windows.GUID
	ID              uint64
	EffectiveWeight wfpValue
}
type filterTemplate struct {
	Provider        *windows.GUID
	Layer           windows.GUID
	EnumType, Flags uint32
	ProviderContext unsafe.Pointer
	Count           uint32
	Conditions      *wfpCondition
	ActionMask      uint32
	Callout         *windows.GUID
}
type v6Mask struct {
	Address [16]byte
	Prefix  uint8
}

const (
	persistentFlag = uint32(1)
	blockAction    = uint32(0x1001)
	permitAction   = uint32(0x1002)
	marker         = "tenebra/persistent-host-guard/v1"
)

// Stable ownership keys. A collision is an error unless the metadata and the
// provider relationship match; no operation deletes by display name.
var providerKey = guid("fcb43b44-9358-4cd7-a998-9e7f822d5248")
var sublayerKey = guid("fcb43b45-9358-4cd7-a998-9e7f822d5248")
var layerKeys = [4]windows.GUID{
	guid("c38d57d1-05a7-4c33-904f-7fbceee60e82"), guid("4a72393b-319f-44bc-84c3-ba54dcb3b6b4"),
	guid("e1cd9fe7-f4b5-4273-96c0-592e487b8650"), guid("a3b42c97-9f04-4672-b87e-cee9c483257f"),
}
var fieldFlags = guid("632ce23b-5167-435c-86d7-e903684aa80c")
var fieldNextHop = guid("93ae8f5b-7f6f-4719-98c8-14e97429ef04")
var fieldLocalInterface = guid("4cd62a49-59c3-4969-b7f3-bda5d32890a4")
var fieldApp = guid("d78e1e87-8644-4ea5-9437-d809ecefc971")
var fieldUser = guid("af043a0a-b34d-4f86-979c-c90371af6e66")
var fieldProtocol = guid("3971ef2b-623e-4f9a-8cb1-6e79b806b9a7")
var fieldLocalPort = guid("0c1ba1af-5765-453f-af22-a8f791ac775b")
var fieldRemotePort = guid("c35a604d-d22b-4e1a-91b4-68f674ee674b")
var fieldRemoteAddress = guid("b235ae9a-1d64-49b8-a44c-5ff3d9095045")

// GUID parsing is pure Go; it does not load a DLL or contact BFE.
func guid(s string) windows.GUID {
	b, err := hex.DecodeString(strings.ReplaceAll(s, "-", ""))
	if err != nil || len(b) != 16 {
		panic(err)
	}
	g := windows.GUID{Data1: binary.BigEndian.Uint32(b[:4]), Data2: binary.BigEndian.Uint16(b[4:6]), Data3: binary.BigEndian.Uint16(b[6:8])}
	copy(g.Data4[:], b[8:])
	return g
}
func filterKey(key string) windows.GUID {
	h := sha256.Sum256([]byte(marker + "/" + key))
	return windows.GUID{Data1: binary.BigEndian.Uint32(h[:4]), Data2: binary.BigEndian.Uint16(h[4:6]), Data3: (binary.BigEndian.Uint16(h[6:8]) & 0x0fff) | 0x5000, Data4: [8]byte{(h[8] & 0x3f) | 0x80, h[9], h[10], h[11], h[12], h[13], h[14], h[15]}}
}

// Pointer arguments remain typed GC roots through the entire synchronous call.
// Do not convert stack pointers to uintptr before entering an ordinary Go
// wrapper: a stack growth could otherwise invalidate the native address.
type nativeArg struct {
	p     unsafe.Pointer
	value uintptr
}

func ptr[T any](p *T) nativeArg { return nativeArg{p: unsafe.Pointer(p)} }
func num(n uintptr) nativeArg   { return nativeArg{value: n} }

type nativeCall func(string, ...nativeArg) error

// Lazy construction performs no native call. Reuse the loaded module instead
// of accumulating a LoadLibrary reference for each filter operation.
var wfpDLL = windows.NewLazySystemDLL("fwpuclnt.dll")

func callWFP(name string, args ...nativeArg) error {
	proc := wfpDLL.NewProc(name)
	if err := proc.Find(); err != nil {
		return err
	}
	values := make([]uintptr, len(args))
	for i, a := range args {
		values[i] = a.value
		if a.p != nil {
			values[i] = uintptr(a.p)
		}
	}
	r, _, _ := proc.Call(values...)
	runtime.KeepAlive(args)
	if name == "FwpmFreeMemory0" {
		return nil
	} // void API
	if r != 0 {
		return fmt.Errorf("%s: %w", name, syscall.Errno(r))
	}
	return nil
}

func transaction(call nativeCall, h uintptr, fn func() error) (err error) {
	if err = call("FwpmTransactionBegin0", num(h), num(0)); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			abortErr := call("FwpmTransactionAbort0", num(h))
			if abortErr != nil {
				err = fmt.Errorf("%w; abort: %v", err, abortErr)
			}
		}
	}()
	if err = fn(); err != nil {
		return err
	}
	if err = call("FwpmTransactionCommit0", num(h)); err != nil {
		return err
	}
	committed = true
	return nil
}
