package securefs

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
)

// This private, deny-only record preserves API immutability after reopening.
// It never authenticates a payload or makes an operator input trustworthy.
const sealName = ".securefs-sealed.json"

type sealRecord struct {
	Version  int    `json:"version"`
	ObjectID string `json:"object_id"`
}

func hasSealComponent(parts []string) bool {
	for _, part := range parts {
		if part == sealName {
			return true
		}
	}
	return false
}

func readSeal(ops nativeOps, directory handle, policy Policy, actor string) (bool, error) {
	h, err := ops.openAt(directory, sealName, openRequest{})
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer ops.close(h)
	info, err := ops.inspect(h)
	if err != nil {
		return false, err
	}
	if err := verifyObject(info, policy, actor, false); err != nil {
		return false, err
	}
	data, err := readBounded(ops, h, 1024)
	if err != nil {
		return false, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var record sealRecord
	if err := decoder.Decode(&record); err != nil {
		return false, ErrUntrusted
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return false, ErrUntrusted
	}
	root, err := ops.inspect(directory)
	if err != nil {
		return false, err
	}
	if record.Version != 1 || record.ObjectID != root.id {
		return false, ErrUntrusted
	}
	return true, nil
}

func writeSeal(root *rootState) error {
	info, err := root.ops.inspect(root.h)
	if err != nil {
		return err
	}
	data, err := json.Marshal(sealRecord{1, info.id})
	if err != nil {
		return err
	}
	h, err := root.ops.openAt(root.h, sealName, openRequest{create: true, write: true, policy: root.policy})
	if err != nil {
		return err
	}
	defer root.ops.close(h)
	marker, err := root.ops.inspect(h)
	if err != nil {
		return err
	}
	if err := verifyObject(marker, root.policy, root.actor, false); err != nil {
		return err
	}
	root.inventory.record(root.prefix+sealName, marker.id)
	for offset := 0; offset < len(data); {
		n, err := root.ops.write(h, data[offset:])
		if err != nil {
			return err
		}
		if n <= 0 || n > len(data)-offset {
			return io.ErrShortWrite
		}
		offset += n
	}
	if err := root.ops.flush(h); err != nil {
		return err
	}
	valid, err := readSeal(root.ops, root.h, root.policy, root.actor)
	if err != nil {
		return err
	}
	if !valid {
		return ErrChanged
	}
	return nil
}
