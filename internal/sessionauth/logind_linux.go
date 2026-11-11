//go:build linux

package sessionauth

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/Divaaaan/tenebra/internal/securefs"
)

type LogindConsoleSource struct{}
type logindQuery func(context.Context, ...string) ([]byte, error)

func (LogindConsoleSource) ActivePhysicalUID(ctx context.Context) (*uint32, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	bound, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	root, err := securefs.OpenRoot("/usr/bin", securefs.Policy{Mode: securefs.OperatorInput, Principal: "uid:0"}, false)
	if err != nil {
		return nil, errors.Join(ErrPhysicalConsole, err)
	}
	_, checkErr := root.ReadFile("loginctl", 32<<20)
	if err := errors.Join(checkErr, root.Close()); err != nil {
		return nil, errors.Join(ErrPhysicalConsole, err)
	}
	return queryPhysicalUID(bound, runLogind)
}

type logindOutput struct{ buffer bytes.Buffer }

func (out *logindOutput) Write(body []byte) (int, error) {
	if out.buffer.Len()+len(body) > 64<<10 {
		return 0, ErrPhysicalConsole
	}
	return out.buffer.Write(body)
}

func runLogind(ctx context.Context, args ...string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	command := exec.CommandContext(ctx, "/usr/bin/loginctl", append([]string{"--no-pager", "--no-legend", "--no-ask-password"}, args...)...)
	command.Env = []string{"PATH=/usr/bin:/bin", "HOME=/var/empty", "LANG=C", "LC_ALL=C", "SYSTEMD_COLORS=0"}
	var out logindOutput
	command.Stdout, command.Stderr = &out, io.Discard
	command.WaitDelay = 100 * time.Millisecond
	if err := command.Run(); err != nil {
		return nil, errors.Join(ErrPhysicalConsole, err)
	}
	return out.buffer.Bytes(), nil
}

func logindName(value string) bool {
	if value == "" || strings.HasPrefix(value, "-") || value == "self" || value == "auto" {
		return false
	}
	for _, letter := range value {
		if !(letter >= 'a' && letter <= 'z' || letter >= 'A' && letter <= 'Z' || letter >= '0' && letter <= '9' || letter == '-' || letter == '_') {
			return false
		}
	}
	return true
}

func queryPhysicalUID(ctx context.Context, query logindQuery) (*uint32, error) {
	list, err := query(ctx, "list-seats")
	if err != nil {
		return nil, err
	}
	var physical *uint32
	seats := make(map[string]bool)
	for _, line := range strings.Split(strings.TrimSpace(string(list)), "\n") {
		seat := strings.TrimSpace(line)
		if seat == "" {
			continue
		}
		if !logindName(seat) || !strings.HasPrefix(seat, "seat") || seats[seat] {
			return nil, ErrPhysicalConsole
		}
		seats[seat] = true
		body, err := query(ctx, "show-seat", seat, "--all", "--property=ActiveSession", "--value")
		if err != nil {
			return nil, err
		}
		session := strings.TrimSpace(string(body))
		if session == "" {
			continue
		}
		if !logindName(session) {
			return nil, ErrPhysicalConsole
		}
		body, err = query(ctx, "show-session", session, "--all", "--property=User", "--property=Active", "--property=Remote", "--property=Seat", "--property=Class", "--property=Type")
		if err != nil {
			return nil, err
		}
		uid, err := physicalSessionUID(body, seat)
		if err != nil {
			return nil, err
		}
		if uid == nil {
			continue
		}
		if physical != nil && *physical != *uid {
			return nil, ErrPhysicalConsole
		}
		physical = uid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return physical, nil
}

func physicalSessionUID(body []byte, seat string) (*uint32, error) {
	values := make(map[string]string, 6)
	for _, line := range strings.Split(strings.TrimSuffix(string(body), "\n"), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return nil, ErrPhysicalConsole
		}
		if _, duplicate := values[key]; duplicate {
			return nil, ErrPhysicalConsole
		}
		switch key {
		case "User", "Active", "Remote", "Seat", "Class", "Type":
			values[key] = value
		default:
			return nil, ErrPhysicalConsole
		}
	}
	if len(values) != 6 || values["Seat"] != seat || values["Active"] != "yes" || values["Remote"] != "no" {
		return nil, ErrPhysicalConsole
	}
	if values["Class"] == "greeter" || values["Class"] == "lock-screen" {
		return nil, nil
	}
	switch values["Class"] {
	case "user", "user-early", "user-light", "user-early-light":
	default:
		return nil, ErrPhysicalConsole
	}
	switch values["Type"] {
	case "x11", "wayland", "mir", "tty":
	default:
		return nil, ErrPhysicalConsole
	}
	uid, err := strconv.ParseUint(values["User"], 10, 32)
	if err != nil || uid == math.MaxUint32 || strconv.FormatUint(uid, 10) != values["User"] {
		return nil, ErrPhysicalConsole
	}
	value := uint32(uid)
	return &value, nil
}
