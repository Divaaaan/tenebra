// Package dnswire speaks DNS to a named resolver.
//
// It exists because two parts of this client have to resolve a name through a
// resolver of their own choosing rather than the machine's: the subscription
// fetcher, which falls back to public DoH when the system resolver answers with
// a poisoned or blocked address, and the packet-filter exclusion list, which has
// to learn the address sing-box will dial for an exit node and therefore has to
// ask the same resolver sing-box asks.
//
// It is deliberately small: one question, one answer, A and AAAA records. It is
// not a caching resolver and knows nothing about search domains or hosts files.
package dnswire

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strings"
)

// DNS record and class type numbers (RFC 1035, RFC 3596).
const (
	TypeA     uint16 = 1
	TypeCNAME uint16 = 5
	TypeAAAA  uint16 = 28
	ClassIN   uint16 = 1
)

// EncodeQuery builds the wire form of a single recursion-desired question for
// host of the given record type. The ID is 0, which RFC 8484 recommends for GET
// cacheability; transports that need an unpredictable ID (plain UDP, where any
// on-path party can forge an answer) set their own — see the udp transport.
func EncodeQuery(host string, qtype uint16) ([]byte, error) {
	name, err := EncodeName(host)
	if err != nil {
		return nil, err
	}
	msg := make([]byte, 0, 12+len(name)+4)
	msg = append(msg,
		0x00, 0x00, // ID
		0x01, 0x00, // flags: recursion desired
		0x00, 0x01, // QDCOUNT
		0x00, 0x00, // ANCOUNT
		0x00, 0x00, // NSCOUNT
		0x00, 0x00, // ARCOUNT
	)
	msg = append(msg, name...)
	msg = binary.BigEndian.AppendUint16(msg, qtype)
	msg = binary.BigEndian.AppendUint16(msg, ClassIN)
	return msg, nil
}

// EncodeName encodes a host as a length-prefixed, root-terminated DNS name.
func EncodeName(host string) ([]byte, error) {
	host = strings.TrimSuffix(host, ".")
	if host == "" {
		return nil, errors.New("dns: empty host")
	}
	var out []byte
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 {
			return nil, fmt.Errorf("dns: invalid label in %q", host)
		}
		out = append(out, byte(len(label)))
		out = append(out, label...)
	}
	out = append(out, 0x00)
	if len(out) > 255 {
		return nil, fmt.Errorf("dns: name too long: %q", host)
	}
	return out, nil
}

// ParseResponse validates a wire response and returns the addresses bound to
// host. It follows the CNAME chain from the queried name (common for CDN-fronted
// names) and only accepts records linked to it, so an answer for an unrelated
// name yields no addresses.
func ParseResponse(msg []byte, host string, qtype uint16) ([]net.IP, error) {
	if len(msg) < 12 {
		return nil, errors.New("dns: short response")
	}
	flags := binary.BigEndian.Uint16(msg[2:4])
	if flags&0x8000 == 0 {
		return nil, errors.New("dns: reply is not a response")
	}
	if rcode := flags & 0x000F; rcode != 0 {
		return nil, fmt.Errorf("dns: resolver returned rcode %d for %q", rcode, host)
	}
	qdcount := int(binary.BigEndian.Uint16(msg[4:6]))
	ancount := int(binary.BigEndian.Uint16(msg[6:8]))

	off := 12
	for i := 0; i < qdcount; i++ {
		_, next, err := ReadName(msg, off)
		if err != nil {
			return nil, err
		}
		off = next + 4 // QTYPE + QCLASS
		if off > len(msg) {
			return nil, errors.New("dns: truncated question")
		}
	}

	cnames := make(map[string]string)
	ipsByName := make(map[string][]net.IP)
	for i := 0; i < ancount; i++ {
		owner, next, err := ReadName(msg, off)
		if err != nil {
			return nil, err
		}
		off = next
		if off+10 > len(msg) {
			return nil, errors.New("dns: truncated record header")
		}
		rtype := binary.BigEndian.Uint16(msg[off : off+2])
		rdlen := int(binary.BigEndian.Uint16(msg[off+8 : off+10]))
		off += 10
		if off+rdlen > len(msg) {
			return nil, errors.New("dns: truncated record data")
		}
		switch rtype {
		case TypeA:
			if rdlen == net.IPv4len {
				ip := make(net.IP, net.IPv4len)
				copy(ip, msg[off:off+rdlen])
				ipsByName[owner] = append(ipsByName[owner], ip)
			}
		case TypeAAAA:
			if rdlen == net.IPv6len {
				ip := make(net.IP, net.IPv6len)
				copy(ip, msg[off:off+rdlen])
				ipsByName[owner] = append(ipsByName[owner], ip)
			}
		case TypeCNAME:
			target, _, err := ReadName(msg, off)
			if err != nil {
				return nil, err
			}
			cnames[owner] = target
		}
		off += rdlen
	}

	ips := followChain(CanonicalHost(host), cnames, ipsByName)
	if len(ips) == 0 {
		return nil, fmt.Errorf("dns: no %s record for %q", TypeName(qtype), host)
	}
	return ips, nil
}

// followChain walks the CNAME chain from start and gathers every address bound
// to a name along it. A visited set and hop cap guard against a malicious or
// broken chain that loops.
func followChain(start string, cnames map[string]string, ipsByName map[string][]net.IP) []net.IP {
	const maxHops = 32
	var ips []net.IP
	seen := make(map[string]bool)
	cur := start
	for i := 0; i < maxHops; i++ {
		if seen[cur] {
			break
		}
		seen[cur] = true
		ips = append(ips, ipsByName[cur]...)
		next, ok := cnames[cur]
		if !ok {
			break
		}
		cur = next
	}
	return ips
}

// ReadName reads a DNS name (following compression pointers) starting at off.
// It returns the lower-cased name without a trailing dot and the offset of the
// first byte after the name in the original stream (i.e. after the first
// pointer, not the pointer target).
func ReadName(msg []byte, off int) (name string, next int, err error) {
	const maxPointers = 32
	var sb strings.Builder
	next = -1
	pointers := 0
	for {
		if off < 0 || off >= len(msg) {
			return "", 0, errors.New("dns: name offset out of range")
		}
		b := msg[off]
		switch b & 0xC0 {
		case 0x00:
			if b == 0x00 {
				off++
				if next < 0 {
					next = off
				}
				return CanonicalHost(sb.String()), next, nil
			}
			length := int(b)
			off++
			if off+length > len(msg) {
				return "", 0, errors.New("dns: truncated label")
			}
			sb.Write(msg[off : off+length])
			sb.WriteByte('.')
			off += length
		case 0xC0:
			if off+1 >= len(msg) {
				return "", 0, errors.New("dns: truncated compression pointer")
			}
			ptr := int(b&0x3F)<<8 | int(msg[off+1])
			if next < 0 {
				next = off + 2
			}
			if ptr >= off {
				// Pointers must reference earlier bytes; anything else risks a
				// loop or reads uninitialised data.
				return "", 0, errors.New("dns: non-backward compression pointer")
			}
			pointers++
			if pointers > maxPointers {
				return "", 0, errors.New("dns: too many compression pointers")
			}
			off = ptr
		default:
			return "", 0, errors.New("dns: reserved label type")
		}
	}
}

// CanonicalHost lower-cases a host and strips a trailing dot so names compare
// consistently regardless of case or root form.
func CanonicalHost(h string) string {
	return strings.TrimSuffix(strings.ToLower(h), ".")
}

// TypeName names a record type for error messages.
func TypeName(qtype uint16) string {
	switch qtype {
	case TypeA:
		return "A"
	case TypeAAAA:
		return "AAAA"
	default:
		return fmt.Sprintf("type-%d", qtype)
	}
}
