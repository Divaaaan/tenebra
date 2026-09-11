//go:build windows && (amd64 || arm64)

package protection

import (
	"errors"
	"net/netip"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

type windowsBackend struct {
	enginePath func() (string, error)
	call       nativeCall
}

// NewWindowsBackend is inert: no DLL is loaded, no interface is enumerated and
// no WFP session is opened until an explicit Guard operation. Production alone
// passes this backend to the daemon. Unit fixtures use an in-memory Backend.
func NewWindowsBackend(enginePath func() (string, error)) Backend {
	return &windowsBackend{enginePath: enginePath, call: callWFP}
}

func (b *windowsBackend) session(fn func(uintptr) error) error {
	s := wfpSession{Timeout: 3000}
	var h uintptr
	if err := b.call("FwpmEngineOpen0", num(0), num(10), num(0), ptr(&s), ptr(&h)); err != nil {
		return err
	}
	defer b.call("FwpmEngineClose0", num(h))
	return fn(h)
}
func (b *windowsBackend) free(p unsafe.Pointer) { b.call("FwpmFreeMemory0", ptr(&p)) }
func blobEqual(v byteBlob, s string) bool {
	return v.Size == uint32(len(s)) && v.Data != nil && string(unsafe.Slice(v.Data, v.Size)) == s
}
func makeBlob(s []byte) byteBlob {
	if len(s) == 0 {
		return byteBlob{}
	}
	return byteBlob{Size: uint32(len(s)), Data: &s[0]}
}
func display(name string) displayData {
	return displayData{Name: windows.StringToUTF16Ptr(name), Description: windows.StringToUTF16Ptr(marker)}
}
func notFound(err error, code windows.Handle) bool { return errors.Is(err, syscall.Errno(code)) }

// owned validates both stable keys before any deletion. A partially-created
// provider is recoverable; an occupied key with foreign metadata is not ours.
func (b *windowsBackend) owned(h uintptr) (provider, sublayer bool, err error) {
	s, err := b.ownedState(h)
	return s.provider, s.sublayer, err
}

type ownership struct{ provider, sublayer, enforcing bool }

// Ownership and current enforcement are separate. Disabled/static/misweighted
// owned objects must remain removable and atomically repairable.
func (b *windowsBackend) ownedState(h uintptr) (out ownership, err error) {
	var p *wfpProvider
	err = b.call("FwpmProviderGetByKey0", num(h), ptr(&providerKey), ptr(&p))
	if err != nil && !notFound(err, windows.FWP_E_PROVIDER_NOT_FOUND) {
		return out, err
	}
	if err == nil {
		defer b.free(unsafe.Pointer(p))
		if p == nil || p.Key != providerKey || !blobEqual(p.Data, marker) {
			return out, errors.New("WFP provider ownership mismatch")
		}
		out.provider = true
		out.enforcing = p.Flags&persistentFlag != 0 && p.Flags&0x10 == 0 && (p.ServiceName == nil || windows.UTF16PtrToString(p.ServiceName) == "")
	}
	var s *wfpSublayer
	err = b.call("FwpmSubLayerGetByKey0", num(h), ptr(&sublayerKey), ptr(&s))
	if err != nil && !notFound(err, windows.FWP_E_SUBLAYER_NOT_FOUND) {
		return out, err
	}
	if err == nil {
		defer b.free(unsafe.Pointer(s))
		if s == nil || s.Key != sublayerKey || s.Provider == nil || *s.Provider != providerKey || !blobEqual(s.Data, marker) {
			return out, errors.New("WFP sublayer ownership mismatch")
		}
		out.sublayer = true
		out.enforcing = out.enforcing && s.Flags&persistentFlag != 0 && s.Weight == 0xffff
	}
	if out.sublayer && !out.provider {
		return out, errors.New("WFP sublayer has no owned provider")
	}
	return out, nil
}

type filterInfo struct {
	key, layer           windows.GUID
	flags, action, count uint32
	weight               uint64
}

func (b *windowsBackend) filters(h uintptr) ([]filterInfo, error) {
	var out []filterInfo
	for _, layer := range layerKeys {
		filters, err := b.filtersAtLayer(h, layer)
		if err != nil {
			return nil, err
		}
		if len(out)+len(filters) > 512 {
			return nil, errors.New("too many owned WFP filters")
		}
		out = append(out, filters...)
	}
	return out, nil
}

func (b *windowsBackend) filtersAtLayer(h uintptr, layer windows.GUID) ([]filterInfo, error) {
	// SDK fwptypes.h: INCLUDE_BOOTTIME=8, INCLUDE_DISABLED=16. Cleanup must
	// enumerate these too, even though they are not evidence of active policy.
	template := filterTemplate{Provider: &providerKey, Layer: layer, Flags: 0x18, ActionMask: 0xffffffff}
	var enum uintptr
	if err := b.call("FwpmFilterCreateEnumHandle0", num(h), ptr(&template), ptr(&enum)); err != nil {
		return nil, err
	}
	defer b.call("FwpmFilterDestroyEnumHandle0", num(h), num(enum))
	var out []filterInfo
	for batch := 0; batch < 9; batch++ {
		var entries **wfpFilter
		var count uint32
		if err := b.call("FwpmFilterEnum0", num(h), num(enum), num(64), ptr(&entries), ptr(&count)); err != nil {
			return nil, err
		}
		if count > 64 {
			b.free(unsafe.Pointer(entries))
			return nil, errors.New("WFP enumeration exceeded batch bound")
		}
		if count == 0 {
			if entries != nil {
				b.free(unsafe.Pointer(entries))
			}
			return out, nil
		}
		if entries == nil {
			return nil, errors.New("WFP returned a nil filter array")
		}
		var invalid error
		for _, f := range unsafe.Slice(entries, count) {
			if f == nil || f.Provider == nil || *f.Provider != providerKey || f.Sublayer != sublayerKey || f.Layer != layer || !blobEqual(f.Data, marker) {
				invalid = errors.New("refusing foreign filter in Tenebra provider")
				break
			}
			var weight uint64
			if f.Weight.Type == 4 && f.Weight.Value != 0 {
				weight = *(*uint64)(f.Weight.pointer())
			}
			out = append(out, filterInfo{f.Key, f.Layer, f.Flags, f.Action.Type, f.Count, weight})
		}
		b.free(unsafe.Pointer(entries))
		if invalid != nil {
			return nil, invalid
		}
	}
	return nil, errors.New("too many owned WFP filters; explicit recovery required")
}

func completeBlocks(filters []filterInfo) bool {
	seen := map[windows.GUID]bool{}
	for _, r := range Policy(0) {
		if len(r.Conditions) != 0 {
			continue
		}
		key := filterKey(r.Key)
		for _, f := range filters {
			if f.key == key && f.layer == layerKeys[r.Layer] && f.flags&1 != 0 && f.flags&0x22 == 0 && f.action == blockAction && f.count == 0 && f.weight == 1 {
				seen[key] = true
			}
		}
	}
	return len(seen) == 4
}

func (b *windowsBackend) Inspect() (present, complete bool, err error) {
	err = b.session(func(h uintptr) error {
		owned, e := b.ownedState(h)
		if e != nil {
			return e
		}
		present = owned.provider || owned.sublayer
		if !owned.provider {
			return nil
		}
		filters, e := b.filters(h)
		if e != nil {
			return e
		}
		complete = owned.sublayer && owned.enforcing && completeBlocks(filters)
		return nil
	})
	return
}

func (b *windowsBackend) Replace(luid uint64) error {
	identities, err := b.identities()
	if err != nil {
		return err
	}
	// Explicit system/admin-only object ACL; no ordinary user can widen or remove
	// a persistent exception. Independent firewalls retain their own policies.
	sd, err := windows.SecurityDescriptorFromString("O:SYG:SYD:P(A;;GA;;;SY)(A;;GA;;;BA)")
	if err != nil {
		return err
	}
	return b.session(func(h uintptr) error {
		return transaction(b.call, h, func() error {
			// Recreate the foundation as well as filters in the SAME transaction.
			// This repairs a legitimate provider disabled by BFE; disabled is an
			// output-only flag and cannot be cleared by changing an Add argument.
			if err := b.removeOwned(h); err != nil {
				return err
			}
			data := []byte(marker)
			provider := wfpProvider{Key: providerKey, Display: display("Tenebra persistent host protection"), Flags: 1, Data: makeBlob(data)}
			if err := b.call("FwpmProviderAdd0", num(h), ptr(&provider), ptr(sd)); err != nil {
				return err
			}
			sub := wfpSublayer{Key: sublayerKey, Display: display("Tenebra host protection"), Flags: 1, Provider: &providerKey, Data: makeBlob(data), Weight: 0xffff}
			if err := b.call("FwpmSubLayerAdd0", num(h), ptr(&sub), ptr(sd)); err != nil {
				return err
			}
			for _, r := range Policy(luid) {
				if err := b.addFilter(h, r, identities, sd); err != nil {
					return err
				}
			}
			runtime.KeepAlive(data)
			return nil
		})
	})
}

func (b *windowsBackend) Remove() error {
	return b.session(func(h uintptr) error {
		return transaction(b.call, h, func() error { return b.removeOwned(h) })
	})
}

func (b *windowsBackend) removeOwned(h uintptr) error {
	p, s, err := b.owned(h)
	if err != nil {
		return err
	}
	if !p && !s {
		return nil
	}
	old, err := b.filters(h)
	if err != nil {
		return err
	}
	for _, f := range old {
		key := f.key
		if err := b.call("FwpmFilterDeleteByKey0", num(h), ptr(&key)); err != nil {
			return err
		}
	}
	if s {
		if err := b.call("FwpmSubLayerDeleteByKey0", num(h), ptr(&sublayerKey)); err != nil {
			return err
		}
	}
	if p {
		return b.call("FwpmProviderDeleteByKey0", num(h), ptr(&providerKey))
	}
	return nil
}

type appIdentity struct {
	app []byte
	sd  []byte // self-relative descriptor, wrapped in FWP_BYTE_BLOB for conditions
}

func (b *windowsBackend) addFilter(h uintptr, r Rule, identities map[string]appIdentity, sd *windows.SECURITY_DESCRIPTOR) error {
	data := []byte(marker)
	weight := new(uint64)
	*weight = r.Weight
	f := wfpFilter{Key: filterKey(r.Key), Display: display("Tenebra " + r.Key), Flags: 1, Provider: &providerKey, Data: makeBlob(data), Layer: layerKeys[r.Layer], Sublayer: sublayerKey, Weight: wfpValue{Type: 4, Value: uintptr(unsafe.Pointer(weight))}, Action: wfpAction{Type: blockAction}}
	if r.Permit {
		f.Action.Type = permitAction
	} // soft permit: no CLEAR_ACTION_RIGHT
	var conditions []wfpCondition
	roots := []any{weight, data, identities}
	add := func(field windows.GUID, match, typ uint32, value uintptr) {
		conditions = append(conditions, wfpCondition{field, match, wfpValue{typ, value}})
	}
	for _, c := range r.Conditions {
		switch c.Field {
		case Loopback:
			add(fieldFlags, 6, 3, 1) // FWP_MATCH_FLAGS_ALL_SET
		case Interface:
			v := new(uint64)
			*v = c.Number
			roots = append(roots, v)
			// Reauthorization uses the ORIGINAL flow layer in both packet
			// directions. An inbound-established flow also needs its outgoing
			// reply path constrained; local interface alone can be stale.
			add(fieldNextHop, 0, 4, uintptr(unsafe.Pointer(v)))
			if !r.Layer.Outbound() {
				add(fieldLocalInterface, 0, 4, uintptr(unsafe.Pointer(v)))
			}
		case Application:
			id, ok := identities[c.Text]
			if !ok || len(id.app) == 0 || len(id.sd) == 0 {
				return errors.New("missing trusted WFP app identity")
			}
			blob := &byteBlob{Size: uint32(len(id.app)), Data: &id.app[0]}
			sdBlob := &byteBlob{Size: uint32(len(id.sd)), Data: &id.sd[0]}
			roots = append(roots, blob, sdBlob)
			add(fieldApp, 0, 12, uintptr(unsafe.Pointer(blob)))
			add(fieldUser, 0, 14, uintptr(unsafe.Pointer(sdBlob)))
		case Protocol:
			add(fieldProtocol, 0, 1, uintptr(c.Number))
		case LocalPort:
			add(fieldLocalPort, 0, 2, uintptr(c.Number))
		case RemotePort:
			add(fieldRemotePort, 0, 2, uintptr(c.Number))
		case RemoteAddress:
			prefix, err := netip.ParsePrefix(c.Text)
			if err != nil || !prefix.Addr().Is6() {
				return errors.New("invalid NDP prefix")
			}
			mask := &v6Mask{prefix.Addr().As16(), uint8(prefix.Bits())}
			roots = append(roots, mask)
			add(fieldRemoteAddress, 0, 257, uintptr(unsafe.Pointer(mask)))
		default:
			return errors.New("unsupported WFP condition")
		}
	}
	if len(conditions) > 0 {
		f.Conditions = &conditions[0]
		f.Count = uint32(len(conditions))
	}
	var id uint64
	err := b.call("FwpmFilterAdd0", num(h), ptr(&f), ptr(sd), ptr(&id))
	runtime.KeepAlive(roots)
	return err
}
