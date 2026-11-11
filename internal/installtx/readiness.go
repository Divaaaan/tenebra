package installtx

import (
	"context"
	"errors"
	"time"
)

var ErrNotReady = errors.New("identified installer process is not ready")

type ReadyRequest struct {
	Schema    uint32 `json:"schema"`
	Cmd       string `json:"cmd"`
	TxID      string `json:"tx_id"`
	Challenge string `json:"challenge"`
}
type ReadyReply struct {
	Schema            uint32 `json:"schema"`
	StoreSchema       uint32 `json:"store_schema"`
	Status            string `json:"status"`
	TxID              string `json:"tx_id"`
	Challenge         string `json:"challenge"`
	DaemonVersion     string `json:"daemon_version"`
	DaemonInstanceID  string `json:"daemon_instance_id"`
	LaunchID          string `json:"launch_id"`
	CoreSHA256        string `json:"core_sha256"`
	Transport         string `json:"transport"`
	BusinessAdmission string `json:"business_admission"`
	StoreRevision     uint64 `json:"store_revision"`
}
type ReadyReceipt = ReadyReply
type ReadyExpectation struct {
	Schema, StoreSchema                                                                                                 uint32
	TxID, DaemonVersion, DaemonInstanceID, PreviousDaemonInstanceID, LaunchID, CoreSHA256, Transport, BusinessAdmission string
	StoreRevision                                                                                                       uint64
	Process                                                                                                             ProcessIdentity
}

// Exchange must authenticate the kernel peer as root, expected PID and live
// process start token before sending a challenge. Reply contents alone cannot
// establish process identity. Implementations must honor the request context.
type ReadyProbe interface {
	Exchange(context.Context, ProcessIdentity, ReadyRequest) (ReadyReply, error)
}

func VerifyReady(ctx context.Context, probe ReadyProbe, expected ReadyExpectation) (ReadyReceipt, error) {
	if probe == nil || expected.Schema != 1 || expected.StoreSchema == 0 || expected.StoreRevision == 0 ||
		!protocolID(expected.TxID, "it1_") || !hexValue(expected.LaunchID, 32) ||
		expected.Process.PID == 0 || expected.Process.StartToken == "" || expected.Process.LaunchID != expected.LaunchID ||
		!hexValue(expected.CoreSHA256, 64) || !safeToken(expected.DaemonVersion, 128) ||
		expected.BusinessAdmission != "closed" || !oneOf(expected.Transport, "unix") {
		return ReadyReceipt{}, ErrReadiness
	}
	overall, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	for {
		if err := overall.Err(); err != nil {
			return ReadyReceipt{}, err
		}
		challenge, err := newID("")
		if err != nil {
			return ReadyReceipt{}, err
		}
		request := ReadyRequest{Schema: 1, Cmd: "ready", TxID: expected.TxID, Challenge: challenge}
		attempt, stop := context.WithTimeout(overall, 2*time.Second)
		reply, err := probe.Exchange(attempt, expected.Process, request)
		attemptErr := attempt.Err()
		stop()
		if err == nil && attemptErr == nil {
			if reply.Schema != expected.Schema || reply.StoreSchema != expected.StoreSchema || reply.Status != "ready" ||
				reply.TxID != expected.TxID || reply.Challenge != challenge || reply.LaunchID != expected.LaunchID ||
				reply.DaemonVersion != expected.DaemonVersion || reply.CoreSHA256 != expected.CoreSHA256 ||
				reply.Transport != expected.Transport || reply.BusinessAdmission != expected.BusinessAdmission ||
				reply.StoreRevision < expected.StoreRevision || !safeToken(reply.DaemonInstanceID, 128) || len(reply.DaemonInstanceID) < 20 ||
				reply.DaemonInstanceID == expected.PreviousDaemonInstanceID ||
				expected.DaemonInstanceID != "" && reply.DaemonInstanceID != expected.DaemonInstanceID {
				return ReadyReceipt{}, ErrReadiness
			}
			return reply, nil
		}
		if err == nil {
			err = attemptErr
		}
		if !errors.Is(err, ErrNotReady) && !errors.Is(err, context.DeadlineExceeded) {
			return ReadyReceipt{}, err
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-overall.Done():
			timer.Stop()
			return ReadyReceipt{}, overall.Err()
		case <-timer.C:
		}
	}
}
