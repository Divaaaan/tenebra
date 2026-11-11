//go:build linux || darwin

package installtx

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net"
	"path/filepath"
	"time"

	"github.com/Divaaaan/tenebra/internal/securefs"
)

type readySocketProbe struct{ path string }

func NewReadyProbe(readyRoot *securefs.Root, txID string) (ReadyProbe, error) {
	if readyRoot == nil || !protocolID(txID, "it1_") {
		return nil, ErrReadiness
	}
	path, err := readyRoot.Path()
	if err != nil {
		return nil, err
	}
	return &readySocketProbe{path: filepath.Join(path, txID+".sock")}, nil
}
func (p *readySocketProbe) Exchange(ctx context.Context, process ProcessIdentity, request ReadyRequest) (ReadyReply, error) {
	if p == nil || !filepath.IsAbs(p.path) || process.PID == 0 || process.StartToken == "" ||
		!hexValue(process.LaunchID, 32) || request.Schema != 1 || request.Cmd != "ready" || !protocolID(request.TxID, "it1_") || !hexValue(request.Challenge, 32) {
		return ReadyReply{}, ErrReadiness
	}
	return p.exchangeRequest(ctx, process, request)
}
func (p *readySocketProbe) exchangeRequest(ctx context.Context, process ProcessIdentity, request any) (ReadyReply, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", p.path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, errReadyConnectionRefused) {
			return ReadyReply{}, errors.Join(ErrNotReady, err)
		}
		return ReadyReply{}, err
	}
	defer conn.Close()
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		return ReadyReply{}, ErrReadiness
	}
	if err := authenticateReadyPeer(unixConn, process); err != nil {
		return ReadyReply{}, err
	}
	deadline := time.Now().Add(2 * time.Second)
	if end, ok := ctx.Deadline(); ok && end.Before(deadline) {
		deadline = end
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return ReadyReply{}, err
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	data, err := json.Marshal(request)
	if err != nil {
		return ReadyReply{}, ErrProtocol
	}
	if _, err := conn.Write(append(data, '\n')); err != nil {
		return ReadyReply{}, err
	}
	line, err := bufio.NewReaderSize(conn, MaxMetadataBytes+1).ReadSlice('\n')
	if err != nil {
		if errors.Is(err, io.EOF) {
			return ReadyReply{}, ErrReadiness
		}
		return ReadyReply{}, err
	}
	if len(line) > MaxMetadataBytes {
		return ReadyReply{}, ErrReadiness
	}
	var reply ReadyReply
	if strictDecode(line, &reply) != nil {
		return ReadyReply{}, ErrReadiness
	}
	if err := authenticateReadyPeer(unixConn, process); err != nil {
		return ReadyReply{}, err
	}
	return reply, nil
}

func (p *readySocketProbe) admit(ctx context.Context, process ProcessIdentity, request PrivateRequest, expected ReadyReceipt) (ReadyReply, error) {
	if request.validate() != nil || request.Cmd != "admit" || expected.Schema != 1 || expected.Status != "ready" || expected.TxID != request.TxID ||
		expected.LaunchID != process.LaunchID || expected.BusinessAdmission != "closed" || expected.StoreSchema == 0 || expected.StoreRevision == 0 ||
		!hexValue(expected.CoreSHA256, 64) || !safeToken(expected.DaemonVersion, 128) || len(expected.DaemonInstanceID) < 20 {
		return ReadyReply{}, ErrReadiness
	}
	reply, err := p.exchangeRequest(ctx, process, request)
	if err != nil {
		return ReadyReply{}, err
	}
	if reply.Schema != expected.Schema || reply.Status != "admitted" || reply.TxID != expected.TxID || reply.Challenge != "" ||
		reply.DaemonVersion != expected.DaemonVersion || reply.DaemonInstanceID != expected.DaemonInstanceID ||
		reply.LaunchID != expected.LaunchID || reply.CoreSHA256 != expected.CoreSHA256 || reply.Transport != expected.Transport ||
		reply.StoreSchema != expected.StoreSchema || reply.StoreRevision < expected.StoreRevision || reply.BusinessAdmission != "open" {
		return ReadyReply{}, ErrReadiness
	}
	return reply, nil
}

func (p *readySocketProbe) fence(ctx context.Context, process ProcessIdentity, request PrivateRequest, old ReadyReceipt) (ReadyReply, error) {
	if request.validate() != nil || request.Cmd != "fence" || request.TxID == old.TxID || !protocolID(old.TxID, "it1_") ||
		old.Schema != 1 || !oneOf(old.Status, "ready", "admitted") || !oneOf(old.BusinessAdmission, "open", "closed") ||
		old.LaunchID != process.LaunchID || !hexValue(old.LaunchID, 32) || process.PID == 0 || process.StartToken == "" ||
		old.StoreSchema == 0 || old.StoreRevision == 0 || old.Transport != "unix" ||
		!hexValue(old.CoreSHA256, 64) || !safeToken(old.DaemonVersion, 128) || !safeToken(old.DaemonInstanceID, 128) || len(old.DaemonInstanceID) < 20 {
		return ReadyReply{}, ErrReadiness
	}
	reply, err := p.exchangeRequest(ctx, process, request)
	if err != nil {
		return ReadyReply{}, err
	}
	if reply.Schema != old.Schema || reply.Status != "fenced" || reply.TxID != request.TxID || reply.Challenge != request.Challenge ||
		reply.DaemonVersion != old.DaemonVersion || reply.DaemonInstanceID != old.DaemonInstanceID ||
		reply.LaunchID != old.LaunchID || reply.CoreSHA256 != old.CoreSHA256 || reply.Transport != old.Transport ||
		reply.StoreSchema != old.StoreSchema || reply.StoreRevision < old.StoreRevision || reply.BusinessAdmission != "closed" {
		return ReadyReply{}, ErrReadiness
	}
	return reply, nil
}

func authenticateReadyPeer(conn *net.UnixConn, expected ProcessIdentity) error {
	raw, err := conn.SyscallConn()
	if err != nil {
		return err
	}
	var check error
	if err := raw.Control(func(fd uintptr) { check = verifyReadyPeerFD(int(fd), expected) }); err != nil {
		return err
	}
	return check
}
