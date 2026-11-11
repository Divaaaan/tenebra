package installtx

import (
	"errors"
	"io"
	"path/filepath"
	"strings"
)

var (
	ErrHelperPrivilege   = errors.New("helper command requires effective uid0")
	ErrHelperAckRequired = errors.New("explicit untrusted input acknowledgement required")
)

// HelperCommand contains only the fixed P0 command union. Its request is data,
// never authority for a principal, vendor key, destination, service or program.
type HelperCommand struct {
	Name, Manager, TransactionID string
	AcknowledgeUntrusted         bool
	Request                      *ApplyRequest
}

func ParseHelperCommand(args []string, input io.Reader, effectiveUID int) (HelperCommand, error) {
	var c HelperCommand
	if len(args) == 0 {
		return c, ErrProtocol
	}
	c.Name = args[0]
	if c.Name == "status" {
		if len(args) != 2 || args[1] != "--json" {
			return HelperCommand{}, ErrProtocol
		}
		return c, nil // Public projection only; do not read privileged stdin.
	}
	if !oneOf(c.Name, "apply", "stage-package", "remove", "recover", "service-host") {
		return HelperCommand{}, ErrProtocol
	}
	// Check the actual kernel EUID before reading even one request byte.
	if effectiveUID != 0 {
		return HelperCommand{}, ErrHelperPrivilege
	}
	switch c.Name {
	case "service-host":
		if len(args) != 1 {
			return HelperCommand{}, ErrProtocol
		}
		return c, nil
	case "recover":
		if len(args) != 2 || !strings.HasPrefix(args[1], "--transaction=") {
			return HelperCommand{}, ErrProtocol
		}
		c.TransactionID = strings.TrimPrefix(args[1], "--transaction=")
		if !protocolID(c.TransactionID, "it1_") {
			return HelperCommand{}, ErrProtocol
		}
		return c, nil
	case "stage-package":
		if len(args) != 2 || !strings.HasPrefix(args[1], "--manager=") {
			return HelperCommand{}, ErrProtocol
		}
		c.Manager = strings.TrimPrefix(args[1], "--manager=")
		if !oneOf(c.Manager, "dpkg", "pacman") {
			return HelperCommand{}, ErrProtocol
		}
		return c, nil
	}
	flags := map[string]bool{}
	for _, flag := range args[1:] {
		if flags[flag] {
			return HelperCommand{}, ErrProtocol
		}
		flags[flag] = true
		if flag != "--request-stdin" && !(c.Name == "apply" && flag == "--ack-untrusted-input") && !(c.Name == "remove" && flag == "--keep-profiles") {
			return HelperCommand{}, ErrProtocol
		}
	}
	if !flags["--request-stdin"] || c.Name == "remove" && !flags["--keep-profiles"] || input == nil {
		return HelperCommand{}, ErrProtocol
	}
	body, err := io.ReadAll(io.LimitReader(input, MaxMetadataBytes+1))
	if err != nil {
		return HelperCommand{}, err
	}
	if len(body) > MaxMetadataBytes {
		return HelperCommand{}, ErrProtocol
	}
	request, err := DecodeApplyRequest(body)
	if err != nil {
		return HelperCommand{}, err
	}
	if c.Name == "remove" && request.Action != "remove" || c.Name == "apply" && request.Action == "remove" {
		return HelperCommand{}, ErrProtocol
	}
	c.AcknowledgeUntrusted = flags["--ack-untrusted-input"]
	dev := request.Action == "install" && request.Trust == "operator_dev"
	if dev && !c.AcknowledgeUntrusted {
		return HelperCommand{}, ErrHelperAckRequired
	}
	if !dev && c.AcknowledgeUntrusted {
		return HelperCommand{}, ErrProtocol
	}
	if source := request.Source; source != nil {
		if !filepath.IsAbs(source.AbsolutePath) || source.SignaturePath != "" && !filepath.IsAbs(source.SignaturePath) {
			return HelperCommand{}, ErrProtocol
		}
	}
	c.Request = &request
	return c, nil
}
