package sessionauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"slices"
	"time"
)

var ErrOperatorPolicy = errors.New("invalid Linux operator policy")
var ErrPhysicalConsole = errors.New("active physical console is unavailable or ambiguous")

type ConsoleSource interface {
	ActivePhysicalUID(context.Context) (*uint32, error)
}

type OperatorPolicy interface {
	Load(context.Context) ([]uint32, error)
}

type AdmissionSet struct {
	ConsoleUID   *uint32
	OperatorUIDs []uint32
}

func (a AdmissionSet) Allows(uid uint32) bool {
	return a.ConsoleUID != nil && *a.ConsoleUID == uid || slices.Contains(a.OperatorUIDs, uid)
}

func ResolveLinux(ctx context.Context, console ConsoleSource, policy OperatorPolicy) (AdmissionSet, error) {
	if console == nil || policy == nil {
		return AdmissionSet{}, ErrOperatorPolicy
	}
	if err := ctx.Err(); err != nil {
		return AdmissionSet{}, err
	}
	uids, err := policy.Load(ctx)
	if err != nil {
		return AdmissionSet{}, err
	}
	set := AdmissionSet{OperatorUIDs: slices.Clone(uids)}
	for _, uid := range set.OperatorUIDs {
		if uid == math.MaxUint32 {
			return AdmissionSet{}, ErrOperatorPolicy
		}
	}
	bound, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	uid, err := console.ActivePhysicalUID(bound)
	if err == nil {
		err = bound.Err()
	}
	if err != nil {
		if len(set.OperatorUIDs) == 0 {
			return AdmissionSet{}, err
		}
		return set, ctx.Err()
	}
	if uid != nil {
		if *uid == math.MaxUint32 {
			return AdmissionSet{}, ErrPhysicalConsole
		}
		value := *uid
		set.ConsoleUID = &value
	}
	return set, ctx.Err()
}

func decodeOperatorPolicy(body []byte) ([]uint32, error) {
	if len(body) == 0 || len(body) > 64<<10 {
		return nil, ErrOperatorPolicy
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	first, err := decoder.Token()
	if err != nil || first != json.Delim('{') {
		return nil, ErrOperatorPolicy
	}
	seen := make(map[string]bool, 2)
	var schema uint32
	var uids []uint32
	for decoder.More() {
		token, err := decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok || seen[key] {
			return nil, ErrOperatorPolicy
		}
		seen[key] = true
		switch key {
		case "schema":
			err = decoder.Decode(&schema)
		case "uids":
			err = decoder.Decode(&uids)
		default:
			return nil, ErrOperatorPolicy
		}
		if err != nil {
			return nil, ErrOperatorPolicy
		}
	}
	last, err := decoder.Token()
	if err != nil || last != json.Delim('}') || schema != 1 || !seen["schema"] || !seen["uids"] || uids == nil {
		return nil, ErrOperatorPolicy
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, ErrOperatorPolicy
	}
	slices.Sort(uids)
	for index, uid := range uids {
		if uid == math.MaxUint32 || index > 0 && uid == uids[index-1] {
			return nil, ErrOperatorPolicy
		}
	}
	return uids, nil
}
