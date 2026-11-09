package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func socketServer(t *testing.T, respond func(net.Conn)) string {
	t.Helper()
	// macOS limits sockaddr_un to 104 bytes; t.TempDir's default prefix is longer.
	dir, err := os.MkdirTemp("/tmp", "tenebra-hc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		c.SetDeadline(time.Now().Add(time.Second))
		line, err := bufio.NewReader(c).ReadBytes('\n')
		if err != nil {
			return
		}
		var req struct {
			ID  int64  `json:"id"`
			Cmd string `json:"cmd"`
		}
		if json.Unmarshal(line, &req) != nil || req.ID != requestID || req.Cmd != "status" {
			t.Error("checker must send a numeric-id read-only status request")
			return
		}
		respond(c)
	}()
	return path
}

func TestStatusReply(t *testing.T) {
	tests := []struct {
		name, reply string
		wantOK      bool
	}{
		{"ready", `{"id":1,"ok":true,"data":{"daemon_version":"0.6.1"}}`, true},
		{"event-before-reply", "{\"event\":\"state\",\"data\":{\"profile\":\"PRIVATE_PROFILE\"}}\n" + `{"id":1,"ok":true,"data":{"daemon_version":"0.6.1"}}`, true},
		{"daemon-error", `{"id":1,"ok":false,"error":"PRIVATE_PROFILE"}`, false},
		{"wrong-id", `{"id":2,"ok":true,"data":{"daemon_version":"0.6.1"}}`, false},
		{"string-id", `{"id":"1","ok":true,"data":{"daemon_version":"0.6.1"}}`, false},
		{"missing-ok", `{"id":1,"data":{"daemon_version":"0.6.1"}}`, false},
		{"old-version", `{"id":1,"ok":true,"data":{"daemon_version":"0.6.0"}}`, false},
		{"missing-version", `{"id":1,"ok":true,"data":{}}`, false},
		{"malformed", `not-json PRIVATE_PROFILE`, false},
		{"oversized", strings.Repeat("x", maxLineBytes+1), false},
		{"too-many-events", strings.Repeat("{\"event\":\"log\",\"message\":\"PRIVATE_PROFILE\"}\n", maxEvents+1), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := socketServer(t, func(c net.Conn) { c.Write([]byte(tc.reply + "\n")) })
			var out, stderr bytes.Buffer
			code := run([]string{"--socket", path, "--timeout", "300ms"}, &out, &stderr)
			if (code == 0) != tc.wantOK {
				t.Fatalf("code=%d out=%q err=%q", code, out.String(), stderr.String())
			}
			if strings.Contains(out.String()+stderr.String(), "PRIVATE_PROFILE") {
				t.Fatal("leaked daemon payload")
			}
		})
	}
}

func TestTimeoutAndStaleSocket(t *testing.T) {
	t.Run("timeout", func(t *testing.T) {
		path := socketServer(t, func(c net.Conn) { time.Sleep(200 * time.Millisecond) })
		start := time.Now()
		if err := check(path, "0.6.1", 30*time.Millisecond); err == nil {
			t.Fatal("unresponsive daemon accepted")
		}
		if time.Since(start) > 180*time.Millisecond {
			t.Fatal("deadline not enforced")
		}
	})
	t.Run("stale", func(t *testing.T) {
		dir, err := os.MkdirTemp("/tmp", "tenebra-hc-")
		if err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(dir)
		path := filepath.Join(dir, "s")
		ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
		if err != nil {
			t.Fatal(err)
		}
		ln.SetUnlinkOnClose(false)
		ln.Close()
		if err := check(path, "0.6.1", 30*time.Millisecond); err == nil {
			t.Fatal("stale socket accepted")
		}
	})
}

func TestRejectUnboundedTimeout(t *testing.T) {
	for _, timeout := range []string{"0s", "-1s", "1h"} {
		if run([]string{"--timeout", timeout}, &bytes.Buffer{}, &bytes.Buffer{}) == 0 {
			t.Fatal("unbounded timeout accepted")
		}
	}
}
