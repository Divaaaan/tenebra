//go:build windows && (amd64 || arm64)

package protection

import (
	"errors"
	"reflect"
	"syscall"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestWFPABI64WithoutNativeCalls(t *testing.T) {
	f := wfpFilter{}
	checks := map[string][2]uintptr{
		"filter size": {unsafe.Sizeof(f), 200}, "context": {unsafe.Offsetof(f.Context), 152},
		"reserved": {unsafe.Offsetof(f.Reserved), 168}, "id": {unsafe.Offsetof(f.ID), 176},
		"effective": {unsafe.Offsetof(f.EffectiveWeight), 184}, "value": {unsafe.Sizeof(wfpValue{}), 16},
		"condition": {unsafe.Sizeof(wfpCondition{}), 40}, "session": {unsafe.Sizeof(wfpSession{}), 72},
		"provider": {unsafe.Sizeof(wfpProvider{}), 64}, "sublayer": {unsafe.Sizeof(wfpSublayer{}), 72},
	}
	for name, c := range checks {
		if c[0] != c[1] {
			t.Errorf("%s=%d want%d", name, c[0], c[1])
		}
	}
}

func TestWFPMarshalsOnlyScopedPersistentSoftPermits(t *testing.T) {
	apps := map[string]appIdentity{}
	for _, name := range []string{Core, Engine, DHCP} {
		apps[name] = appIdentity{app: []byte{1, 2}, sd: []byte{3, 4, 5}}
	}
	var got []filterInfo
	b := &windowsBackend{call: func(name string, args ...nativeArg) error {
		if name != "FwpmFilterAdd0" {
			t.Fatal("unexpected native call", name)
		}
		f := (*wfpFilter)(args[1].p)
		if f.Provider == nil || *f.Provider != providerKey || f.Sublayer != sublayerKey || f.Flags != 1 || f.Context != [2]uint64{} {
			t.Fatal("wrong filter lifetime/ownership")
		}
		got = append(got, filterInfo{f.Key, f.Layer, f.Flags, f.Action.Type, f.Count, *(*uint64)(f.Weight.pointer())})
		if f.Count == 0 {
			return nil
		}
		hasNextHop, hasLocal := false, false
		for _, c := range unsafe.Slice(f.Conditions, f.Count) {
			if c.Field == fieldUser {
				blob := (*byteBlob)(c.Value.pointer())
				if c.Value.Type != 14 || blob.Size != 3 || blob.Data == nil || *blob.Data != 3 {
					t.Fatal("security descriptor is not an FWP_BYTE_BLOB")
				}
			}
			if c.Field == fieldNextHop || c.Field == fieldLocalInterface {
				hasNextHop = hasNextHop || c.Field == fieldNextHop
				hasLocal = hasLocal || c.Field == fieldLocalInterface
				if c.Value.Type != 4 || *(*uint64)(c.Value.pointer()) != 42 {
					t.Fatal("wrong TUN identity condition")
				}
			}
		}
		if hasNextHop || hasLocal {
			inbound := f.Layer == layerKeys[Accept4] || f.Layer == layerKeys[Accept6]
			if !hasNextHop || hasLocal != inbound {
				t.Fatal("TUN permit does not constrain both directions of the flow")
			}
		}
		return nil
	}}
	for _, r := range Policy(42) {
		if err := b.addFilter(7, r, apps, nil); err != nil {
			t.Fatal(err)
		}
	}
	if !completeBlocks(got) {
		t.Fatal("native form does not cover four default blocks")
	}
	got[0].flags = 0 // a non-block does not establish enforcement itself
	for i := range got {
		if got[i].count == 0 {
			got[i].flags |= 32
			break
		}
	}
	if completeBlocks(got) {
		t.Fatal("disabled catch-all reported complete")
	}
}

func TestWFPOwnershipCollisionNeverDeletesForeignObjects(t *testing.T) {
	data := []byte("not-tenebra")
	p := &wfpProvider{Key: providerKey, Flags: 1, Data: makeBlob(data)}
	var calls []string
	b := &windowsBackend{call: func(name string, args ...nativeArg) error {
		calls = append(calls, name)
		switch name {
		case "FwpmProviderGetByKey0":
			*(**wfpProvider)(args[2].p) = p
			return nil
		case "FwpmFreeMemory0":
			return nil
		default:
			return syscall.Errno(windows.FWP_E_SUBLAYER_NOT_FOUND)
		}
	}}
	if _, _, err := b.owned(7); err == nil {
		t.Fatal("foreign provider adopted")
	}
	if !reflect.DeepEqual(calls, []string{"FwpmProviderGetByKey0", "FwpmFreeMemory0"}) {
		t.Fatal("foreign provider was touched", calls)
	}
}

func TestWFPDisabledOwnedPolicyCanBeRecoveredAndRemoved(t *testing.T) {
	data := []byte(marker)
	p := &wfpProvider{Key: providerKey, Flags: 1 | 0x10, Data: makeBlob(data)}
	s := &wfpSublayer{Key: sublayerKey, Flags: 1, Provider: &providerKey, Data: makeBlob(data), Weight: 0xffff}
	b := &windowsBackend{call: func(name string, args ...nativeArg) error {
		switch name {
		case "FwpmProviderGetByKey0":
			*(**wfpProvider)(args[2].p) = p
		case "FwpmSubLayerGetByKey0":
			*(**wfpSublayer)(args[2].p) = s
		case "FwpmFreeMemory0":
		default:
			t.Fatal("unexpected call", name)
		}
		return nil
	}}
	if _, _, err := b.owned(7); err != nil {
		t.Fatal("disabled legitimate provider became unremovable", err)
	}
	stop := errors.New("fake enumeration boundary")
	b.call = func(name string, args ...nativeArg) error {
		if name != "FwpmFilterCreateEnumHandle0" {
			t.Fatal(name)
		}
		template := (*filterTemplate)(args[1].p)
		if template.Flags&0x18 != 0x18 {
			t.Fatal("cleanup omits disabled/boot-time filters")
		}
		if template.Layer != layerKeys[Connect4] {
			t.Fatal("enumeration must use a specific owned layer")
		}
		return stop
	}
	if _, err := b.filters(7); !errors.Is(err, stop) {
		t.Fatal(err)
	}
}

func TestWFPTransactionCommitAndFailureAbortWithoutNativeCalls(t *testing.T) {
	for _, fail := range []string{"", "operation", "FwpmTransactionBegin0", "FwpmTransactionCommit0"} {
		t.Run(fail, func(t *testing.T) {
			var calls []string
			call := func(name string, _ ...nativeArg) error {
				calls = append(calls, name)
				if name == fail {
					return errors.New("injected")
				}
				return nil
			}
			err := transaction(call, 7, func() error { return call("operation") })
			want := []string{"FwpmTransactionBegin0", "operation", "FwpmTransactionCommit0"}
			switch fail {
			case "operation":
				want = []string{"FwpmTransactionBegin0", "operation", "FwpmTransactionAbort0"}
			case "FwpmTransactionBegin0":
				want = []string{"FwpmTransactionBegin0"}
			case "FwpmTransactionCommit0":
				want = append(want, "FwpmTransactionAbort0")
			}
			if !reflect.DeepEqual(calls, want) || (err == nil) != (fail == "") {
				t.Fatalf("calls=%v err=%v", calls, err)
			}
		})
	}
}
