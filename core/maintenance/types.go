// Package maintenance owns per-operation network capabilities. It has no
// dependency on daemon, persistent state, a system resolver, or an OS dialer.
package maintenance

import (
	"context"
	"io"
	"net"
	"net/netip"
	"time"
)

type RunID string

// InboundConfig is private engine material, never part of a public DTO.
type InboundConfig struct {
	RunID          RunID          `json:"-"`
	Address        netip.AddrPort `json:"-"`
	CertificatePEM string         `json:"-"`
	PrivateKeyPEM  string         `json:"-"`
	Username       string         `json:"-"`
	Password       string         `json:"-"`
}

type RuntimeOptions struct {
	Now  func() time.Time
	Rand io.Reader
}
type Egress string

const (
	VPNOnly           Egress = "vpn_only"
	DirectOnce        Egress = "direct_once"
	UnprotectedDirect Egress = "unprotected_direct"
)

// NumericDialer must pass each raw connection through register before sending
// TLS, authentication, CONNECT, or origin bytes, and use the returned connection.
type NumericDialer func(context.Context, string, netip.AddrPort, func(net.Conn) (net.Conn, error)) (net.Conn, error)

type Endpoint interface {
	RunID() RunID
	DialNumeric(context.Context, string, netip.AddrPort, func(net.Conn) (net.Conn, error)) (net.Conn, error)
}

type LeaseSpec struct {
	OperationID, SessionID, ProfileID string
	ProfileRevision                   uint64
	EngineRunID                       RunID
	AcceptedEpoch, ProtectionEpoch    uint64
	Egress                            Egress
	Deadline                          time.Time
}
