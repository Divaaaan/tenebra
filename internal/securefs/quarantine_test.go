package securefs

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

type receiptBeforeDetachNative struct {
	nativeOps
	vault *Root
	seen  bool
}

func (n *receiptBeforeDetachNative) rename(source, parent handle, name string, replace bool) error {
	data, err := n.vault.ReadFile("quarantine-index.json", 64<<10)
	if err != nil {
		return err
	}
	var index struct {
		Version int      `json:"version"`
		IDs     []string `json:"ids"`
	}
	if err := json.Unmarshal(data, &index); err != nil {
		return err
	}
	if index.Version != 1 || len(index.IDs) != 1 {
		return ErrUntrusted
	}
	data, err = n.vault.ReadFile("quarantine-"+index.IDs[0]+".json", 16<<10)
	if err != nil {
		return err
	}
	var record quarantineRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return err
	}
	if record.ID != index.IDs[0] || record.State != "pending_detach" || record.Acknowledged {
		return ErrUntrusted
	}
	n.seen = true
	return errInjected
}

func TestIncompleteReceiptBlocksRecoveryAfterRestart(t *testing.T) {
	vaultPath := privateFixtureDir(t)
	vault, err := OpenRoot(vaultPath, Policy{Mode: SameAccountPrivate}, false)
	if err != nil {
		t.Fatal(err)
	}
	ops, err := newNativeOps()
	if err != nil {
		t.Fatal(err)
	}
	guard := &receiptBeforeDetachNative{nativeOps: ops, vault: vault}
	source, err := openRootWithOps(privateFixtureDir(t), Policy{Mode: SameAccountPrivate}, false, guard)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	if _, err := source.ReplaceFile("inert.json", []byte("KEEP_SOURCE")); err != nil {
		t.Fatal(err)
	}
	receipt, err := source.Quarantine("inert.json", vault)
	if !errors.Is(err, errInjected) || !guard.seen || receipt.ID == "" {
		t.Fatalf("durable index/receipt was not written before detach: receipt=%q seen=%v error=%v", receipt.ID, guard.seen, err)
	}
	if data, err := source.ReadFile("inert.json", 64); err != nil || string(data) != "KEEP_SOURCE" {
		t.Fatalf("failed detach damaged source: %q %v", data, err)
	}
	if err := vault.Close(); err != nil {
		t.Fatal(err)
	}
	vault, err = OpenRoot(vaultPath, Policy{Mode: SameAccountPrivate}, false)
	if err != nil {
		t.Fatal(err)
	}
	defer vault.Close()
	data, err := vault.ReadFile("quarantine-index.json", 64<<10)
	if err != nil {
		t.Fatal(err)
	}
	var index struct {
		IDs []string `json:"ids"`
	}
	if err := json.Unmarshal(data, &index); err != nil || len(index.IDs) != 1 || index.IDs[0] != receipt.ID {
		t.Fatalf("restart lost recovery fence: %q %v", data, err)
	}
	data, err = vault.ReadFile("quarantine-"+receipt.ID+".json", 16<<10)
	if err != nil {
		t.Fatal(err)
	}
	var record quarantineRecord
	if err := json.Unmarshal(data, &record); err != nil || record.Acknowledged || record.State != "pending_detach" {
		t.Fatalf("incomplete quarantine was accepted: %q %v", data, err)
	}
	// A service's normal writer lock must coexist with the separate quarantine
	// lock; the latter serializes the durable index across independent opens.
	lock, err := vault.LockWriter(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
}
