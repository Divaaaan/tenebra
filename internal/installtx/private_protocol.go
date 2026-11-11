package installtx

import (
	"context"
	"time"
)

type PrivateRequest struct {
	Schema              uint32 `json:"schema"`
	Cmd                 string `json:"cmd"`
	TxID                string `json:"tx_id"`
	Challenge           string `json:"challenge,omitempty"`
	OperationalCommitID string `json:"operational_commit_id,omitempty"`
}

func DecodePrivateRequest(data []byte) (PrivateRequest, error) {
	var request PrivateRequest
	if err := strictDecode(data, &request); err != nil {
		return request, err
	}
	var fields map[string]jsonValue
	if decodeObject(data, &fields) != nil {
		return request, ErrProtocol
	}
	allowed := []string{"schema", "cmd", "tx_id"}
	switch request.Cmd {
	case "ready", "fence":
		allowed = append(allowed, "challenge")
	case "admit":
		allowed = append(allowed, "operational_commit_id")
	default:
		return request, ErrProtocol
	}
	if len(fields) != len(allowed) {
		return request, ErrProtocol
	}
	for name := range fields {
		if !oneOf(name, allowed...) {
			return request, ErrProtocol
		}
	}
	return request, request.validate()
}
func (r PrivateRequest) validate() error {
	if r.Schema != 1 || !protocolID(r.TxID, "it1_") {
		return ErrProtocol
	}
	switch r.Cmd {
	case "ready", "fence":
		if !hexValue(r.Challenge, 32) || r.OperationalCommitID != "" {
			return ErrProtocol
		}
	case "admit":
		if !protocolID(r.OperationalCommitID, "oc1_") || r.Challenge != "" {
			return ErrProtocol
		}
	default:
		return ErrProtocol
	}
	return nil
}

type PrivateEndpoint struct {
	TxID string
	Info func(context.Context) (ReadyReply, error)
	// Admit must reread the protected durable journal and compare the exact
	// transaction, active candidate and operational ID before opening any writer.
	Admit func(context.Context, string, string) error
	// Fence authorizes the NEW transaction against the durable journal and
	// drains this identified OLD process before returning success.
	Fence func(context.Context, string) error
}

type privateAdmissionClient interface {
	admit(context.Context, ProcessIdentity, PrivateRequest, ReadyReceipt) (ReadyReply, error)
}

type privateFenceClient interface {
	fence(context.Context, ProcessIdentity, PrivateRequest, ReadyReceipt) (ReadyReply, error)
}

// The probe still points at the OLD process's endpoint; only the authenticated
// request/reply name the NEW durable transaction.
func FencePrivate(ctx context.Context, probe ReadyProbe, process ProcessIdentity, txID string, old ReadyReceipt) (ReadyReply, error) {
	client, ok := probe.(privateFenceClient)
	if !ok {
		return ReadyReply{}, ErrReadiness
	}
	challenge, err := newID("")
	if err != nil {
		return ReadyReply{}, err
	}
	request := PrivateRequest{Schema: 1, Cmd: "fence", TxID: txID, Challenge: challenge}
	one, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return client.fence(one, process, request, old)
}

func AdmitPrivate(ctx context.Context, probe ReadyProbe, process ProcessIdentity, txID, operationalID string, expected ReadyReceipt) (ReadyReply, error) {
	client, ok := probe.(privateAdmissionClient)
	if !ok {
		return ReadyReply{}, ErrReadiness
	}
	request := PrivateRequest{Schema: 1, Cmd: "admit", TxID: txID, OperationalCommitID: operationalID}
	one, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return client.admit(one, process, request, expected)
}

func (e PrivateEndpoint) handle(ctx context.Context, request PrivateRequest) (ReadyReply, error) {
	if request.validate() != nil || e.Info == nil || e.Admit == nil {
		return ReadyReply{}, ErrProtocol
	}
	if request.Cmd == "fence" {
		if request.TxID == e.TxID || e.Fence == nil {
			return ReadyReply{}, ErrProtocol
		}
	} else if request.TxID != e.TxID {
		return ReadyReply{}, ErrProtocol
	}
	if err := ctx.Err(); err != nil {
		return ReadyReply{}, err
	}
	if request.Cmd == "admit" {
		if err := e.Admit(ctx, request.TxID, request.OperationalCommitID); err != nil {
			return ReadyReply{}, err
		}
	}
	if request.Cmd == "fence" {
		if err := e.Fence(ctx, request.TxID); err != nil {
			return ReadyReply{}, err
		}
	}
	reply, err := e.Info(ctx)
	if err != nil {
		return ReadyReply{}, err
	}
	if reply.Schema != 1 || reply.StoreSchema == 0 || reply.StoreRevision == 0 || !safeToken(reply.DaemonVersion, 128) ||
		!safeToken(reply.DaemonInstanceID, 128) || len(reply.DaemonInstanceID) < 20 || !hexValue(reply.LaunchID, 32) ||
		!hexValue(reply.CoreSHA256, 64) || reply.Transport != "unix" || !oneOf(reply.BusinessAdmission, "closed", "open") {
		return ReadyReply{}, ErrReadiness
	}
	reply.TxID = e.TxID
	reply.Challenge = request.Challenge
	if request.Cmd == "fence" {
		if reply.BusinessAdmission != "closed" {
			return ReadyReply{}, ErrReadiness
		}
		reply.TxID = request.TxID
		reply.Status = "fenced"
	} else if request.Cmd == "ready" {
		reply.Status = "ready"
	} else {
		if reply.BusinessAdmission != "open" {
			return ReadyReply{}, ErrReadiness
		}
		reply.Status = "admitted"
	}
	return reply, nil
}
