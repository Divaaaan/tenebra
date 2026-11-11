package installtx

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Divaaaan/tenebra/internal/securefs"
)

type exactProcessProbe struct {
	expected ProcessIdentity
	request  ReadyRequest
}

func (p exactProcessProbe) Exchange(_ context.Context, process ProcessIdentity, request ReadyRequest) (ReadyReply, error) {
	if process != p.expected || request != p.request {
		return ReadyReply{}, ErrReadiness
	}
	return ReadyReply{TxID: request.TxID, Challenge: request.Challenge}, nil
}
func TestMaintenanceRootProbeRoutesExactTransactionAndRetainsKernelExpectation(t *testing.T) {
	root := privateInstallerRoot(t)
	probe, err := NewMaintenanceReadyProbe(root)
	if err != nil {
		t.Fatal(err)
	}
	p := probe.(*rootReadyProbe)
	expected := ProcessIdentity{PID: 42, StartToken: "1234", LaunchID: strings.Repeat("c", 32)}
	var transactions []string
	for _, suffix := range []string{"a", "b"} {
		request := ReadyRequest{Schema: 1, Cmd: "ready", TxID: "it1_" + strings.Repeat(suffix, 32), Challenge: strings.Repeat("d", 32)}
		p.factory = func(actualRoot *securefs.Root, tx string) (ReadyProbe, error) {
			if actualRoot != root || tx != request.TxID {
				t.Fatal("probe route escaped protected root or transaction")
			}
			transactions = append(transactions, tx)
			return exactProcessProbe{expected: expected, request: request}, nil
		}
		reply, err := probe.Exchange(context.Background(), expected, request)
		if err != nil || reply.TxID != request.TxID || reply.Challenge != request.Challenge {
			t.Fatal(reply, err)
		}
	}
	if len(transactions) != 2 || transactions[0] == transactions[1] {
		t.Fatal("probe remained stuck to previous transaction")
	}
	closed, cancel := context.WithCancel(context.Background())
	cancel()
	request := ReadyRequest{Schema: 1, Cmd: "ready", TxID: transactions[0], Challenge: strings.Repeat("d", 32)}
	if _, err := probe.Exchange(closed, expected, request); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	request.TxID = "../foreign.sock"
	if _, err := probe.Exchange(context.Background(), expected, request); err == nil {
		t.Fatal("arbitrary endpoint accepted")
	}
	if len(transactions) != 2 {
		t.Fatal("invalid or cancelled request reached a socket factory")
	}
}
