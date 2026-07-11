package control

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

// StunCheck is the result of the run_stun_check command. It reports whether UDP
// reaches a public STUN server, the machine's reflexive (public) address as that
// server observed it, and a best-effort NAT-mapping classification.
//
// The classification names what matters for peer-to-peer reachability rather
// than the full RFC 3489 cone taxonomy: an endpoint-independent mapping is
// P2P-friendly, an endpoint-dependent one is not. It is honest about the limits
// of a two-server probe — a single answering server yields "unknown" rather than
// a guess, and no answer at all yields "blocked" with udp_ok=false.
type StunCheck struct {
	// UDPOK reports whether at least one STUN server answered over UDP. False
	// means outbound UDP (or at least STUN) appears blocked from this vantage.
	UDPOK bool `json:"udp_ok"`
	// NATType is the mapping classification: one of the NAT* constants.
	NATType string `json:"nat_type"`
	// ExternalIP is the reflexive public IP a STUN server reported, empty when no
	// server answered.
	ExternalIP string `json:"external_ip,omitempty"`
}

// NAT-mapping classifications reported in StunCheck.NATType.
const (
	// NATBlocked means no STUN server answered — outbound UDP looks blocked.
	NATBlocked = "blocked"
	// NATOpen means the reflexive address is one of the machine's own interface
	// addresses: no NAT sits in front of it.
	NATOpen = "open"
	// NATEndpointIndependent means every queried server saw the same external
	// ip:port — the NAT reuses one mapping regardless of destination (a cone-like
	// mapping; P2P-friendly).
	NATEndpointIndependent = "endpoint-independent"
	// NATEndpointDependent means the servers saw different external mappings — the
	// NAT allocates a new port per destination (a symmetric mapping; P2P-hostile).
	NATEndpointDependent = "endpoint-dependent"
	// NATUnknown means a mapping was observed but not enough of them to classify
	// (only one server answered).
	NATUnknown = "unknown"
)

// STUN wire constants (RFC 5389). The magic cookie and message types are fixed;
// the two address attributes are the only ones this minimal client reads.
const (
	stunBindingRequest   = 0x0001
	stunBindingResponse  = 0x0101
	stunMagicCookie      = 0x2112A442
	attrMappedAddress    = 0x0001
	attrXORMappedAddress = 0x0020
	stunHeaderLen        = 20
)

// stunTimeout bounds a single Binding Request/response exchange. STUN answers
// are one small packet, so a short timeout is plenty and keeps a dead server
// from stalling the probe.
const stunTimeout = 3 * time.Second

// stunMapping is the reflexive transport address a STUN server reported: the
// public IP and port it observed a Binding Request arriving from.
type stunMapping struct {
	IP   net.IP
	Port int
}

// stunProbeResult is what a full STUN probe observed from a single local UDP
// socket. Mappings holds one entry per server that answered, in query order;
// LocalIPs are the machine's own interface addresses, used to tell a public
// (un-NATed) host from one behind NAT.
type stunProbeResult struct {
	LocalIPs []net.IP
	Mappings []stunMapping
}

// stunProbeFunc runs Binding Requests against the given servers from one
// persistent UDP socket and returns what they reported. Reusing a single socket
// keeps the local port stable across servers, which is what makes the
// mapping-behaviour comparison meaningful. It is injected on the daemon so the
// NAT logic is unit-testable offline; production uses realStunProbe.
type stunProbeFunc func(ctx context.Context, servers []string) (stunProbeResult, error)

// defaultStunServers are two distinct public STUN endpoints. Two are needed so
// the probe can compare the external mapping across destinations and classify
// the NAT; they are neutral third-party services (no project infrastructure).
var defaultStunServers = []string{
	"stun.l.google.com:19302",
	"stun1.l.google.com:19302",
}

// handleRunStunCheck runs the UDP/STUN reachability and NAT probe.
func (d *Daemon) handleRunStunCheck(ctx context.Context, req Request) Response {
	res := d.runStunCheck(ctx)
	resp, err := newResult(req.ID, res)
	if err != nil {
		return newError(req.ID, err.Error())
	}
	return resp
}

// runStunCheck probes the STUN servers and classifies the result. A probe error
// (no socket, no answers) is not fatal: it degrades to udp_ok=false / "blocked",
// which is the honest reading of "no UDP mapping could be obtained".
func (d *Daemon) runStunCheck(ctx context.Context) StunCheck {
	probe, err := d.stunProbe(ctx, d.stunServers)
	if err != nil {
		d.emitLog(LogWarn, fmt.Sprintf("stun-check: %v", err))
	}
	udpOK, natType, external := classifyNAT(probe.LocalIPs, probe.Mappings)
	res := StunCheck{UDPOK: udpOK, NATType: natType}
	if external != nil {
		res.ExternalIP = external.String()
	}
	return res
}

// classifyNAT derives the UDP-reachability and NAT-mapping verdict from the
// probe's observations. The rules, in order: no mapping at all means UDP is
// blocked; a reflexive address equal to one of our own interface IPs means no
// NAT ("open"); fewer than two mappings can't be compared ("unknown"); two equal
// mappings mean an endpoint-independent (cone) mapping, and two differing ones an
// endpoint-dependent (symmetric) mapping.
func classifyNAT(localIPs []net.IP, mappings []stunMapping) (udpOK bool, natType string, external net.IP) {
	if len(mappings) == 0 {
		return false, NATBlocked, nil
	}
	external = mappings[0].IP
	for _, lip := range localIPs {
		if lip.Equal(external) {
			return true, NATOpen, external
		}
	}
	if len(mappings) < 2 {
		return true, NATUnknown, external
	}
	a, b := mappings[0], mappings[1]
	if a.IP.Equal(b.IP) && a.Port == b.Port {
		return true, NATEndpointIndependent, external
	}
	return true, NATEndpointDependent, external
}

// realStunProbe queries each server in turn from a single UDP socket and returns
// the external mappings they reported. It is best-effort per server: a dead or
// blocked server contributes no mapping rather than failing the whole probe. A
// hard error is returned only when the local socket cannot be opened.
func realStunProbe(ctx context.Context, servers []string) (stunProbeResult, error) {
	res := stunProbeResult{LocalIPs: localInterfaceIPs()}

	var lc net.ListenConfig
	pc, err := lc.ListenPacket(ctx, "udp", ":0")
	if err != nil {
		return res, fmt.Errorf("diagnostics: open stun socket: %w", err)
	}
	defer pc.Close()

	for _, server := range servers {
		m, err := stunExchange(ctx, pc, server)
		if err != nil {
			continue
		}
		res.Mappings = append(res.Mappings, m)
	}
	return res, nil
}

// stunExchange sends one Binding Request to server over pc and returns the
// mapped address from the matching Binding Response. It filters responses by
// transaction ID, so a late reply from a previously queried server on the shared
// socket is ignored rather than mistaken for this server's answer.
func stunExchange(ctx context.Context, pc net.PacketConn, server string) (stunMapping, error) {
	raddr, err := net.ResolveUDPAddr("udp", server)
	if err != nil {
		return stunMapping{}, fmt.Errorf("resolve %s: %w", server, err)
	}

	var txID [12]byte
	if _, err := rand.Read(txID[:]); err != nil {
		return stunMapping{}, fmt.Errorf("stun transaction id: %w", err)
	}
	req := buildBindingRequest(txID)

	deadline := time.Now().Add(stunTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := pc.SetDeadline(deadline); err != nil {
		return stunMapping{}, err
	}

	if _, err := pc.WriteTo(req, raddr); err != nil {
		return stunMapping{}, fmt.Errorf("send to %s: %w", server, err)
	}

	// STUN responses are small; a single MTU-sized buffer holds any Binding
	// Response. Read until the deadline, skipping packets that don't match our
	// transaction (stray or late replies on the shared socket).
	buf := make([]byte, 1280)
	for {
		n, _, err := pc.ReadFrom(buf)
		if err != nil {
			return stunMapping{}, err
		}
		m, perr := parseBindingResponse(txID, buf[:n])
		if perr != nil {
			continue
		}
		return m, nil
	}
}

// buildBindingRequest renders a 20-byte STUN Binding Request with no attributes:
// the type, a zero attribute length, the magic cookie and the transaction ID.
func buildBindingRequest(txID [12]byte) []byte {
	msg := make([]byte, stunHeaderLen)
	binary.BigEndian.PutUint16(msg[0:2], stunBindingRequest)
	binary.BigEndian.PutUint16(msg[2:4], 0) // no attributes
	binary.BigEndian.PutUint32(msg[4:8], stunMagicCookie)
	copy(msg[8:20], txID[:])
	return msg
}

// parseBindingResponse validates a STUN Binding Success Response against the
// request's transaction ID and returns the mapped address it carries. It prefers
// XOR-MAPPED-ADDRESS (RFC 5389) and falls back to the legacy plain
// MAPPED-ADDRESS. Every length is bounds-checked because the input is an
// untrusted packet handed to a privileged process.
func parseBindingResponse(txID [12]byte, msg []byte) (stunMapping, error) {
	if len(msg) < stunHeaderLen {
		return stunMapping{}, errors.New("stun: response shorter than header")
	}
	if binary.BigEndian.Uint16(msg[0:2]) != stunBindingResponse {
		return stunMapping{}, errors.New("stun: not a binding success response")
	}
	if binary.BigEndian.Uint32(msg[4:8]) != stunMagicCookie {
		return stunMapping{}, errors.New("stun: bad magic cookie")
	}
	if !bytes.Equal(msg[8:20], txID[:]) {
		return stunMapping{}, errors.New("stun: transaction id mismatch")
	}
	attrLen := int(binary.BigEndian.Uint16(msg[2:4]))
	if stunHeaderLen+attrLen > len(msg) {
		return stunMapping{}, errors.New("stun: attribute length exceeds packet")
	}

	var xorMapped, mapped *stunMapping
	p := msg[stunHeaderLen : stunHeaderLen+attrLen]
	for len(p) >= 4 {
		atype := binary.BigEndian.Uint16(p[0:2])
		alen := int(binary.BigEndian.Uint16(p[2:4]))
		if 4+alen > len(p) {
			break // truncated attribute value
		}
		val := p[4 : 4+alen]
		switch atype {
		case attrXORMappedAddress:
			if m, err := parseMappedAddress(val, txID, true); err == nil {
				mm := m
				xorMapped = &mm
			}
		case attrMappedAddress:
			if m, err := parseMappedAddress(val, txID, false); err == nil {
				mm := m
				mapped = &mm
			}
		}
		// Attributes are padded to a 4-byte boundary.
		adv := 4 + alen
		if pad := alen % 4; pad != 0 {
			adv += 4 - pad
		}
		if adv > len(p) {
			break
		}
		p = p[adv:]
	}

	if xorMapped != nil {
		return *xorMapped, nil
	}
	if mapped != nil {
		return *mapped, nil
	}
	return stunMapping{}, errors.New("stun: no mapped-address attribute")
}

// parseMappedAddress decodes a (XOR-)MAPPED-ADDRESS attribute value. When xor is
// set the port is XORed with the high 16 bits of the magic cookie and the
// address with the cookie (IPv4) or the cookie followed by the transaction ID
// (IPv6), per RFC 5389 §15.2.
func parseMappedAddress(val []byte, txID [12]byte, xor bool) (stunMapping, error) {
	if len(val) < 4 {
		return stunMapping{}, errors.New("stun: short mapped-address")
	}
	family := val[1]
	port := binary.BigEndian.Uint16(val[2:4])
	if xor {
		port ^= uint16(stunMagicCookie >> 16)
	}

	switch family {
	case 0x01: // IPv4
		if len(val) < 8 {
			return stunMapping{}, errors.New("stun: short IPv4 mapped-address")
		}
		addr := make([]byte, net.IPv4len)
		copy(addr, val[4:8])
		if xor {
			var cookie [4]byte
			binary.BigEndian.PutUint32(cookie[:], stunMagicCookie)
			for i := range addr {
				addr[i] ^= cookie[i]
			}
		}
		return stunMapping{IP: net.IP(addr), Port: int(port)}, nil
	case 0x02: // IPv6
		if len(val) < 20 {
			return stunMapping{}, errors.New("stun: short IPv6 mapped-address")
		}
		addr := make([]byte, net.IPv6len)
		copy(addr, val[4:20])
		if xor {
			var mask [net.IPv6len]byte
			binary.BigEndian.PutUint32(mask[0:4], stunMagicCookie)
			copy(mask[4:16], txID[:])
			for i := range addr {
				addr[i] ^= mask[i]
			}
		}
		return stunMapping{IP: net.IP(addr), Port: int(port)}, nil
	default:
		return stunMapping{}, errors.New("stun: unknown address family")
	}
}

// localInterfaceIPs returns the machine's own interface addresses, used to
// recognise a reflexive address that belongs to this host (no NAT). Best-effort:
// an enumeration error yields nil, which simply disables the "open" verdict.
func localInterfaceIPs() []net.IP {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	ips := make([]net.IP, 0, len(addrs))
	for _, a := range addrs {
		if ipnet, ok := a.(*net.IPNet); ok {
			ips = append(ips, ipnet.IP)
		}
	}
	return ips
}

// SpeedTest is the result of the run_speed_test command: the measured download
// throughput over the active tunnel, the number of bytes the sample actually
// read, and how long that took.
type SpeedTest struct {
	Mbps        float64 `json:"mbps"`
	SampleBytes int64   `json:"sample_bytes"`
	DurationMs  int64   `json:"duration_ms"`
}

// speedSampleBytes is how much the download samples by default: 10 MiB is large
// enough for a stable rate on a fast link yet quick on a slow one.
const defaultSpeedSampleBytes int64 = 10 << 20

// speedDownloadURLFmt is a neutral third-party endpoint that streams exactly the
// requested number of bytes, so the sample size is controlled by the URL. It is
// a public CDN speed endpoint (no project infrastructure), consistent with the
// leak check's use of neutral echo services.
const speedDownloadURLFmt = "https://speed.cloudflare.com/__down?bytes=%d"

// speedTimeout bounds the whole download, so a slow or stalled link fails the
// sample instead of hanging the command.
const speedTimeout = 30 * time.Second

// defaultSpeedStream opens a streaming GET for the speed sample. Traffic goes
// through whatever the OS routes it over, which is the active tunnel while
// connected (auto_route captures it) — the same assumption the leak check's
// httpGet relies on. It is injected into the daemon so the throughput math can be
// unit-tested offline with a canned reader.
func defaultSpeedStream(ctx context.Context, url string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("diagnostics: speed test: %w", err)
	}
	req.Header.Set("User-Agent", leakUserAgent)

	// Timeout covers reading the body too, so the streamed read below is bounded
	// even though it happens after Do returns.
	client := &http.Client{Timeout: speedTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("diagnostics: speed test: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		resp.Body.Close()
		return nil, fmt.Errorf("diagnostics: speed test: unexpected status %s", resp.Status)
	}
	return resp.Body, nil
}

// handleRunSpeedTest gates the speed test on an active connection — the command
// measures throughput through the tunnel, so it is meaningless while idle — then
// runs the sample.
func (d *Daemon) handleRunSpeedTest(ctx context.Context, req Request) Response {
	if st := d.snapshotState(); st.State != StateConnected {
		return newError(req.ID, "speed test requires an active connection")
	}
	res, err := d.runSpeedTest(ctx)
	if err != nil {
		return newError(req.ID, err.Error())
	}
	resp, err := newResult(req.ID, res)
	if err != nil {
		return newError(req.ID, err.Error())
	}
	return resp
}

// runSpeedTest downloads up to speedSampleBytes from the speed endpoint, timing
// the read, and computes the throughput in megabits per second. It reports the
// bytes actually read, so a body shorter than the target still yields a rate
// from what arrived.
func (d *Daemon) runSpeedTest(ctx context.Context) (SpeedTest, error) {
	rc, err := d.speedStream(ctx, d.speedURL)
	if err != nil {
		return SpeedTest{}, fmt.Errorf("speed test: %w", err)
	}
	defer rc.Close()

	start := d.now()
	n, err := io.Copy(io.Discard, io.LimitReader(rc, d.speedSampleBytes))
	elapsed := d.now().Sub(start)
	if n == 0 {
		if err != nil {
			return SpeedTest{}, fmt.Errorf("speed test: read: %w", err)
		}
		return SpeedTest{}, errors.New("speed test: downloaded no data")
	}

	secs := elapsed.Seconds()
	if secs <= 0 {
		return SpeedTest{}, errors.New("speed test: non-positive sample duration")
	}
	return SpeedTest{
		Mbps:        float64(n*8) / secs / 1e6,
		SampleBytes: n,
		DurationMs:  elapsed.Milliseconds(),
	}, nil
}
