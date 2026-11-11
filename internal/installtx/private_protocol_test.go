package installtx

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
)

func TestPrivateProtocolStrictReadyAdmitUnion(t *testing.T) {
	ready := `{"schema":1,"cmd":"ready","tx_id":"it1_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","challenge":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}`
	admit := `{"schema":1,"cmd":"admit","tx_id":"it1_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","operational_commit_id":"oc1_cccccccccccccccccccccccccccccccc"}`
	for _, body := range []string{ready, admit} {
		if _, err := DecodePrivateRequest([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	for _, body := range []string{
		strings.Replace(ready, `"schema":1`, `"schema":1,"schema":1`, 1),
		strings.Replace(ready, `"schema":1`, `"schema":1,"operational_commit_id":""`, 1),
		strings.Replace(admit, `"schema":1`, `"schema":1,"challenge":""`, 1),
		strings.Replace(ready, `"schema":1`, `"Schema":1`, 1),
		strings.Replace(ready, `"cmd":"ready"`, `"cmd":"exec"`, 1),
		strings.Replace(ready, `"schema":1`, `"schema":null`, 1),
		strings.Replace(admit, "oc1_", "it1_", 1),
		ready + "{}", "null",
	} {
		if _, err := DecodePrivateRequest([]byte(body)); err == nil {
			t.Fatal("ambiguous private request accepted")
		}
	}
}

func TestPrivateProtocolFenceUsesNewTransactionAndFreshChallenge(t *testing.T) {
	expected := readyFixture()
	newTx := "it1_" + strings.Repeat("d", 32)
	fence := PrivateRequest{Schema: 1, Cmd: "fence", TxID: newTx, Challenge: strings.Repeat("f", 32)}
	data, err := json.Marshal(fence)
	if err != nil {
		t.Fatal(err)
	}
	if decoded, err := DecodePrivateRequest(data); err != nil || decoded != fence {
		t.Fatal("valid tagged fence request refused", err)
	}
	for _, invalid := range []string{
		strings.Replace(string(data), `"cmd":"fence"`, `"cmd":"fence","operational_commit_id":""`, 1),
		strings.Replace(string(data), `"challenge":"`+fence.Challenge+`"`, `"challenge":""`, 1),
	} {
		if _, err := DecodePrivateRequest([]byte(invalid)); err == nil {
			t.Fatal("ambiguous fence request accepted")
		}
	}
	var calls int
	var closed bool
	endpoint := PrivateEndpoint{TxID: expected.TxID,
		Info: func(context.Context) (ReadyReply, error) {
			reply := readyReply(expected, ReadyRequest{})
			if !closed {
				reply.BusinessAdmission = "open"
			}
			return reply, nil
		},
		Admit: func(context.Context, string, string) error { t.Fatal("fence attempted admission"); return nil },
		Fence: func(_ context.Context, tx string) error {
			calls++
			if tx != newTx {
				return ErrRecoveryRequired
			}
			closed = true
			return nil
		},
	}
	wrong := fence
	wrong.TxID = expected.TxID
	if _, err := endpoint.handle(context.Background(), wrong); err == nil || calls != 0 {
		t.Fatal("old transaction fenced itself", err)
	}
	wrong.TxID = "it1_" + strings.Repeat("e", 32)
	if _, err := endpoint.handle(context.Background(), wrong); err == nil || closed {
		t.Fatal("unauthorized new transaction fenced the process", err)
	}
	reply, err := endpoint.handle(context.Background(), fence)
	if err != nil || !closed || reply.TxID != newTx || reply.Challenge != fence.Challenge || reply.Status != "fenced" || reply.BusinessAdmission != "closed" {
		t.Fatal("fence receipt lost process/transaction binding", reply, err)
	}
}

func TestPrivateEndpointKeepsWriterClosedUntilExactOperationalAuthority(t *testing.T) {
	expected := readyFixture()
	var open atomic.Bool
	var admits atomic.Int32
	endpoint := PrivateEndpoint{TxID: expected.TxID,
		Info: func(context.Context) (ReadyReply, error) {
			reply := readyReply(expected, ReadyRequest{})
			if open.Load() {
				reply.BusinessAdmission = "open"
			}
			return reply, nil
		},
		Admit: func(ctx context.Context, tx, oc string) error {
			admits.Add(1)
			if tx != expected.TxID || oc != "oc1_"+strings.Repeat("c", 32) {
				return ErrRecoveryRequired
			}
			open.Store(true)
			return nil
		},
	}
	request := PrivateRequest{Schema: 1, Cmd: "ready", TxID: expected.TxID, Challenge: strings.Repeat("b", 32)}
	reply, err := endpoint.handle(context.Background(), request)
	if err != nil || reply.Challenge != request.Challenge || reply.Status != "ready" || reply.BusinessAdmission != "closed" || admits.Load() != 0 {
		t.Fatal("ready admitted writer", reply, err)
	}
	request = PrivateRequest{Schema: 1, Cmd: "admit", TxID: expected.TxID, OperationalCommitID: "oc1_" + strings.Repeat("d", 32)}
	if _, err := endpoint.handle(context.Background(), request); err == nil || open.Load() {
		t.Fatal("wrong operational ID admitted")
	}
	request.OperationalCommitID = "oc1_" + strings.Repeat("c", 32)
	reply, err = endpoint.handle(context.Background(), request)
	if err != nil || reply.Status != "admitted" || reply.BusinessAdmission != "open" || reply.TxID != expected.TxID {
		t.Fatal(reply, err)
	}
	data, err := json.Marshal(reply)
	if err != nil || strings.Contains(string(data), "operational_commit_id") {
		t.Fatal("reply exposed an authority field", err)
	}
}
