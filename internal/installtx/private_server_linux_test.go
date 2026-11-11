//go:build linux

package installtx

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPrivateNativeReadyAndAdmitStayBoundToIdentifiedRootProcess(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("positive authenticated service fixture requires isolated root test process")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	root := privateInstallerRoot(t)
	directory, err := root.Path()
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(directory, "p.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	expected := readyFixture()
	expected.Process.PID = uint32(os.Getpid())
	expected.Process.StartToken, err = ProcessStartToken(expected.Process.PID)
	if err != nil {
		t.Fatal(err)
	}
	var open atomic.Bool
	var attempts atomic.Int32
	operational := "oc1_" + strings.Repeat("c", 32)
	endpoint := PrivateEndpoint{TxID: expected.TxID,
		Info: func(context.Context) (ReadyReply, error) {
			reply := readyReply(expected, ReadyRequest{})
			if open.Load() {
				reply.BusinessAdmission = "open"
			}
			return reply, nil
		},
		Admit: func(ctx context.Context, tx, oc string) error {
			attempts.Add(1)
			if tx != expected.TxID || oc != operational {
				return ErrRecoveryRequired
			}
			open.Store(true)
			return nil
		},
	}
	done := make(chan error, 1)
	go func() { done <- ServePrivate(ctx, listener, endpoint) }()
	probe := &readySocketProbe{path: socket}
	receipt, err := VerifyReady(ctx, probe, expected)
	if err != nil || open.Load() || attempts.Load() != 0 {
		t.Fatal("ready reached business admission", receipt, err)
	}
	if _, err := AdmitPrivate(ctx, probe, expected.Process, expected.TxID, "oc1_"+strings.Repeat("d", 32), receipt); err == nil || open.Load() {
		t.Fatal("wrong durable operational ID admitted")
	}
	reply, err := AdmitPrivate(ctx, probe, expected.Process, expected.TxID, operational, receipt)
	if err != nil || !open.Load() || reply.Status != "admitted" {
		t.Fatal("authenticated admission failed", reply, err)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
