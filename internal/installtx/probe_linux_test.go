//go:build linux

package installtx

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReadinessNativeKernelPeerBeforeChallenge(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	root := privateInstallerRoot(t)
	directory, err := root.Path()
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(directory, "r.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	e := readyFixture()
	e.Process.PID = uint32(os.Getpid())
	e.Process.StartToken, err = ProcessStartToken(e.Process.PID)
	if err != nil {
		t.Fatal(err)
	}
	for _, mismatch := range []string{"pid", "start", "none"} {
		t.Run(mismatch, func(t *testing.T) {
			readBytes := make(chan int, 1)
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					readBytes <- -1
					return
				}
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(time.Second))
				line, err := bufio.NewReader(conn).ReadBytes('\n')
				if err != nil {
					readBytes <- len(line)
					return
				}
				var request ReadyRequest
				if json.Unmarshal(line, &request) != nil {
					readBytes <- -2
					return
				}
				reply := readyReply(e, request)
				data, _ := json.Marshal(reply)
				_, err = conn.Write(append(data, '\n'))
				if err != nil && !errors.Is(err, io.EOF) {
					readBytes <- -3
					return
				}
				// The peer must remain live through the second identity check.
				readBytes <- len(line)
				<-ctx.Done()
			}()
			expected := e
			switch mismatch {
			case "pid":
				expected.Process.PID++
			case "start":
				expected.Process.StartToken += "9"
			}
			_, err := VerifyReady(ctx, &readySocketProbe{path: socket}, expected)
			n := <-readBytes
			if mismatch != "none" || os.Geteuid() != 0 {
				if !errors.Is(err, ErrReadiness) || n != 0 {
					t.Fatalf("foreign peer reached challenge: bytes=%d err=%v", n, err)
				}
			} else if err != nil || n <= 0 {
				t.Fatalf("identified root peer refused: bytes=%d err=%v", n, err)
			}
		})
	}
}
