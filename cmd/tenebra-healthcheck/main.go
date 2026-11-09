// tenebra-healthcheck performs a read-only, bounded readiness check. It never
// prints the status body, which may contain private profile and network data.
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"time"

	"github.com/Divaaaan/tenebra/core/buildinfo"
)

const (
	requestID    = int64(1)
	maxLineBytes = 128 * 1024
	maxReadBytes = 512 * 1024
	maxEvents    = 32
)

func check(socket, version string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	c, err := net.DialTimeout("unix", socket, timeout)
	if err != nil {
		return errors.New("daemon socket unavailable")
	}
	defer c.Close()
	if err := c.SetDeadline(deadline); err != nil {
		return errors.New("cannot set daemon deadline")
	}
	if _, err := io.WriteString(c, "{\"id\":1,\"cmd\":\"status\"}\n"); err != nil {
		return errors.New("daemon request failed")
	}
	scanner := bufio.NewScanner(io.LimitReader(c, maxReadBytes))
	scanner.Buffer(make([]byte, 4096), maxLineBytes)
	for events := 0; scanner.Scan(); {
		var reply struct {
			ID    *int64 `json:"id"`
			OK    *bool  `json:"ok"`
			Event string `json:"event"`
			Data  struct {
				Version string `json:"daemon_version"`
			} `json:"data"`
		}
		if json.Unmarshal(scanner.Bytes(), &reply) != nil {
			return errors.New("malformed daemon response")
		}
		if reply.ID == nil && reply.Event != "" && reply.OK == nil {
			events++
			if events > maxEvents {
				return errors.New("daemon event limit exceeded")
			}
			continue
		}
		if reply.ID == nil || *reply.ID != requestID || reply.OK == nil || reply.Event != "" {
			return errors.New("uncorrelated daemon response")
		}
		if !*reply.OK {
			return errors.New("daemon rejected status request")
		}
		if reply.Data.Version != version {
			return errors.New("daemon version does not match installer")
		}
		return nil
	}
	return errors.New("daemon response absent, oversized or timed out")
}

func run(args []string, out, stderr io.Writer) int {
	flags := flag.NewFlagSet("tenebra-healthcheck", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	socket := flags.String("socket", "/var/run/tenebra.sock", "control socket")
	version := flags.String("expected-version", buildinfo.Version, "required daemon version")
	timeout := flags.Duration("timeout", 2*time.Second, "deadline (maximum 30s)")
	if flags.Parse(args) != nil || flags.NArg() != 0 || *version == "" || *timeout <= 0 || *timeout > 30*time.Second {
		fmt.Fprintln(stderr, "healthcheck: invalid arguments")
		return 2
	}
	if err := check(*socket, *version, *timeout); err != nil {
		fmt.Fprintln(stderr, "healthcheck:", err)
		return 1
	}
	fmt.Fprintln(out, "Tenebra daemon ready")
	return 0
}

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }
