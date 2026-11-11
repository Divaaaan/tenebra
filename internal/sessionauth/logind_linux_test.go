//go:build linux

package sessionauth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
)

func TestPhysicalConsoleUsesActiveLocalSeat(t *testing.T) {
	for _, problem := range []string{"valid", "long-seat", "no-seats", "no-session", "remote", "inactive", "different-seat", "ambiguous", "same-user-seats", "greeter", "invalid-uid", "duplicate-property", "unknown-class", "unknown-type", "invalid-seat", "invalid-session"} {
		t.Run(problem, func(t *testing.T) {
			query := func(ctx context.Context, args ...string) ([]byte, error) {
				switch args[0] {
				case "list-seats":
					switch problem {
					case "no-seats":
						return nil, nil
					case "long-seat":
						return []byte("seat" + strings.Repeat("a", 256) + "\n"), nil
					case "invalid-seat":
						return []byte("seat0 --host=foreign\n"), nil
					case "ambiguous", "same-user-seats":
						return []byte("seat0\nseat1\n"), nil
					default:
						return []byte("seat0\n"), nil
					}
				case "show-seat":
					if problem == "no-session" {
						return []byte("\n"), nil
					}
					if problem == "invalid-session" {
						return []byte("auto\n"), nil
					}
					return []byte("session-" + args[1] + "\n"), nil
				case "show-session":
					seat := strings.TrimPrefix(args[1], "session-")
					uid, active, remote, class, kind := "1000", "yes", "no", "user", "wayland"
					switch problem {
					case "remote":
						remote = "yes"
					case "inactive":
						active = "no"
					case "different-seat":
						seat = "seat9"
					case "ambiguous":
						if seat == "seat1" {
							uid = "1001"
						}
					case "greeter":
						class = "greeter"
					case "invalid-uid":
						uid = "-1"
					case "unknown-class":
						class = "user-incomplete"
					case "unknown-type":
						kind = "unspecified"
					}
					body := fmt.Sprintf("User=%s\nActive=%s\nRemote=%s\nSeat=%s\nClass=%s\nType=%s\n", uid, active, remote, seat, class, kind)
					if problem == "duplicate-property" {
						body += "User=1001\n"
					}
					return []byte(body), nil
				default:
					t.Fatalf("unexpected native query %v", args)
					return nil, nil
				}
			}
			uid, err := queryPhysicalUID(context.Background(), query)
			switch problem {
			case "valid", "long-seat", "same-user-seats":
				if err != nil || uid == nil || *uid != 1000 {
					t.Fatal(uid, err)
				}
			case "no-seats", "no-session", "greeter":
				if err != nil || uid != nil {
					t.Fatal(uid, err)
				}
			default:
				if !errors.Is(err, ErrPhysicalConsole) || uid != nil {
					t.Fatal(uid, err)
				}
			}
		})
	}
}

func TestPhysicalConsoleCommandOutputIsBounded(t *testing.T) {
	var out logindOutput
	if _, err := io.Copy(&out, strings.NewReader(strings.Repeat("x", 65<<10))); !errors.Is(err, ErrPhysicalConsole) || out.buffer.Len() > 64<<10 {
		t.Fatal("native output bypassed its bound", out.buffer.Len(), err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := runLogind(ctx, "list-seats"); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled query executed", err)
	}
}
