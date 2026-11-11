package securefs

import (
	"bytes"
	"encoding/binary"
	"strconv"
)

// These are the on-wire XNU attrreference/kauth_filesec layouts, not Go/C
// pointer layouts. Both supported Darwin architectures use little-endian fields.
// References: apple-oss-distributions/xnu bsd/sys/{attr,kauth}.h and
// bsd/vfs/vfs_attrlist.c; no source-path ACL lookup supplies authority.
const (
	darwinSecurityLimit              = 64 << 10
	darwinFilesecMagic               = 0x012cc16d
	darwinExtendedSecurityCapability = 0x400
)

type securityFacts struct{ grants []grant }

func darwinVolumeHasSecurity(data []byte) bool {
	if len(data) != 36 || binary.LittleEndian.Uint32(data) != uint32(len(data)) {
		return false
	}
	// vol_capabilities_attr: capabilities[4], then valid[4]; interface index 1.
	capabilities := binary.LittleEndian.Uint32(data[8:12])
	valid := binary.LittleEndian.Uint32(data[24:28])
	return capabilities&darwinExtendedSecurityCapability != 0 && valid&darwinExtendedSecurityCapability != 0
}

func parseDarwinSecurity(data []byte, ownerUID uint32) (securityFacts, error) {
	var facts securityFacts
	if len(data) < 12 || len(data) > darwinSecurityLimit || binary.LittleEndian.Uint32(data) != uint32(len(data)) {
		return facts, ErrUntrusted
	}
	offset := int64(int32(binary.LittleEndian.Uint32(data[4:8])))
	length := int64(binary.LittleEndian.Uint32(data[8:12]))
	if length == 0 {
		// fgetattrlist's empty ACL reference is meaningful only after the
		// separate SAME-fd volume capability check has established ACL support.
		if len(data) == 12 && (offset == 0 || offset == 8) {
			return facts, nil
		}
		return facts, ErrUntrusted
	}
	if offset != 8 || length < 44 || 4+offset+length != int64(len(data)) {
		return facts, ErrUntrusted
	}
	body := data[12:]
	if binary.LittleEndian.Uint32(body) != darwinFilesecMagic || !bytes.Equal(body[4:36], make([]byte, 32)) {
		return facts, ErrUntrusted
	}
	count := binary.LittleEndian.Uint32(body[36:40])
	flags := binary.LittleEndian.Uint32(body[40:44])
	if flags & ^uint32(1<<17) != 0 { // only NO_INHERIT is presently understood
		return facts, ErrUntrusted
	}
	if count == ^uint32(0) {
		if len(body) != 44 {
			return facts, ErrUntrusted
		}
		return facts, nil
	}
	if count > 128 || uint64(len(body)) != 44+uint64(count)*24 {
		return facts, ErrUntrusted
	}
	for i := uint32(0); i < count; i++ {
		entry := body[44+int(i)*24 : 44+int(i+1)*24]
		principal, err := darwinACLPrincipal(entry[:16], ownerUID)
		if err != nil {
			return securityFacts{}, err
		}
		aceFlags := binary.LittleEndian.Uint32(entry[16:20])
		kind := aceFlags & 15
		if (kind != 1 && kind != 2) || aceFlags & ^uint32(15|0x1f0) != 0 {
			return securityFacts{}, ErrUntrusted
		}
		rights := binary.LittleEndian.Uint32(entry[20:24])
		const read = uint32(1<<1 | 1<<3 | 1<<7 | 1<<9 | 1<<11 | 1<<22 | 1<<24)
		const mutate = uint32(1<<2 | 1<<4 | 1<<5 | 1<<6 | 1<<8 | 1<<10 | 1<<12 | 1<<13 | 1<<23)
		const all = uint32(1 << 21)
		const synchronize = uint32(1 << 20)
		if rights & ^uint32(read|mutate|all|synchronize) != 0 {
			return securityFacts{}, ErrUntrusted
		}
		var effective accessRights
		if rights&(read|all) != 0 {
			effective |= rightRead
		}
		if rights&(mutate|all) != 0 {
			effective |= rightMutate | rightCreate
		}
		facts.grants = append(facts.grants, grant{principal: principal, rights: effective, deny: kind == 2, inheritOnly: aceFlags&(1<<8) != 0})
	}
	return facts, nil
}

func darwinACLPrincipal(guid []byte, ownerUID uint32) (string, error) {
	if len(guid) != 16 {
		return "", ErrUntrusted
	}
	wellKnown := []byte{0xab, 0xcd, 0xef, 0xab, 0xcd, 0xef, 0xab, 0xcd, 0xef, 0xab, 0xcd, 0xef}
	if bytes.Equal(guid[:12], wellKnown) {
		switch binary.BigEndian.Uint32(guid[12:]) {
		case 12:
			return "posix:other", nil
		case 10:
			return "uid:" + strconv.FormatUint(uint64(ownerUID), 10), nil
		case 16:
			return "posix:group", nil
		case ^uint32(1):
			return "darwin:nobody", nil
		default:
			// Do not reinterpret an unknown directory-service GUID as a
			// numeric group just because the prefix happens to match.
			return "", ErrUntrusted
		}
	}
	// UUID membership resolution is deliberately not guessed, including the
	// compatibility UID form when an external directory resolver is active.
	return "", ErrUntrusted
}

func darwinSystemAlias(name, target string) bool {
	switch name {
	case "var", "tmp", "etc":
		return target == "private/"+name || target == "/private/"+name
	default:
		return false
	}
}
