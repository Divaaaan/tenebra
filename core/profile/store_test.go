package profile

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/tenebra-vpn/tenebra/core/model"
)

func mustProfile(t *testing.T, name, source, url string, nodes []model.Node) Profile {
	t.Helper()
	p, err := NewProfile(name, source, url, nodes)
	if err != nil {
		t.Fatalf("NewProfile: %v", err)
	}
	return p
}

func TestOpenCreatesDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "store")
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("dir not created: %v", err)
	}
	if got := s.List(); len(got) != 0 {
		t.Errorf("fresh store not empty: %v", got)
	}
}

func TestOpenEmptyDirRejected(t *testing.T) {
	if _, err := Open(""); err == nil {
		t.Error("Open(\"\") should fail")
	}
}

func TestAddGetListRemove(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	p1 := mustProfile(t, "A", SourceSubscription, "https://a.test", sampleNodes())
	p2 := mustProfile(t, "B", SourceManual, "", sampleNodes()[:1])

	if err := s.Add(p1); err != nil {
		t.Fatalf("Add p1: %v", err)
	}
	if err := s.Add(p2); err != nil {
		t.Fatalf("Add p2: %v", err)
	}

	if got := s.List(); len(got) != 2 {
		t.Fatalf("List len = %d, want 2", len(got))
	}

	got, ok := s.Get(p1.ID)
	if !ok {
		t.Fatalf("Get(%s) not found", p1.ID)
	}
	if !reflect.DeepEqual(got, p1) {
		t.Errorf("Get returned %+v, want %+v", got, p1)
	}

	if _, ok := s.Get("does-not-exist"); ok {
		t.Error("Get of missing ID returned ok")
	}

	if err := s.Remove(p1.ID); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, ok := s.Get(p1.ID); ok {
		t.Error("profile still present after Remove")
	}
	if got := s.List(); len(got) != 1 || got[0].ID != p2.ID {
		t.Errorf("after Remove, List = %+v, want only p2", got)
	}
}

func TestAddDuplicateRejected(t *testing.T) {
	s, _ := Open(t.TempDir())
	p := mustProfile(t, "A", SourceManual, "", nil)
	if err := s.Add(p); err != nil {
		t.Fatalf("Add: %v", err)
	}
	err := s.Add(p)
	if !errors.Is(err, ErrExists) {
		t.Errorf("duplicate Add err = %v, want ErrExists", err)
	}
}

func TestAddEmptyIDRejected(t *testing.T) {
	s, _ := Open(t.TempDir())
	if err := s.Add(Profile{Name: "no id"}); err == nil {
		t.Error("Add with empty ID should fail")
	}
}

func TestUpdateMissing(t *testing.T) {
	s, _ := Open(t.TempDir())
	p := mustProfile(t, "A", SourceManual, "", nil)
	err := s.Update(p)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("Update missing err = %v, want ErrNotFound", err)
	}
}

func TestRemoveMissing(t *testing.T) {
	s, _ := Open(t.TempDir())
	err := s.Remove("nope")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("Remove missing err = %v, want ErrNotFound", err)
	}
}

// TestRoundTrip writes profiles with one Store and reads them back with a fresh
// Store opened on the same directory.
func TestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s1, err := Open(dir)
	if err != nil {
		t.Fatalf("Open s1: %v", err)
	}
	p1 := mustProfile(t, "A", SourceSubscription, "https://a.test/sub", sampleNodes())
	p2 := mustProfile(t, "B", SourceManual, "", sampleNodes()[:2])
	if err := s1.Add(p1); err != nil {
		t.Fatal(err)
	}
	if err := s1.Add(p2); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("Open s2: %v", err)
	}
	got := s2.List()
	if len(got) != 2 {
		t.Fatalf("reloaded %d profiles, want 2", len(got))
	}
	// Compare via JSON to normalise time zones from the round-trip.
	for i, want := range []Profile{p1, p2} {
		wb, _ := json.Marshal(want)
		gb, _ := json.Marshal(got[i])
		if string(wb) != string(gb) {
			t.Errorf("profile %d round-trip mismatch:\n got %s\nwant %s", i, gb, wb)
		}
	}
}

func TestOpenMissingFileIsEmpty(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if got := s.List(); len(got) != 0 {
		t.Errorf("missing file should yield empty store, got %v", got)
	}
}

func TestOpenCorruptFileFails(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, storeFile), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir); err == nil {
		t.Error("Open of corrupt file should fail")
	}
}

func TestOpenEmptyFileIsEmpty(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, storeFile), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open empty file: %v", err)
	}
	if got := s.List(); len(got) != 0 {
		t.Errorf("empty file should yield empty store, got %v", got)
	}
}

// TestAtomicSaveNoTempLeftover verifies the save leaves exactly profiles.json
// and no temp files, and that the file content is valid JSON.
func TestAtomicSaveNoTempLeftover(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.Add(mustProfile(t, "A", SourceManual, "", sampleNodes())); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
	if len(names) != 1 || names[0] != storeFile {
		t.Errorf("dir contents = %v, want only %s", names, storeFile)
	}

	data, err := os.ReadFile(filepath.Join(dir, storeFile))
	if err != nil {
		t.Fatal(err)
	}
	var check []Profile
	if err := json.Unmarshal(data, &check); err != nil {
		t.Errorf("saved file is not valid profile JSON: %v", err)
	}
	if len(check) != 1 || check[0].Name != "A" {
		t.Errorf("saved content unexpected: %s", data)
	}
}

// TestListReturnsCopy ensures callers cannot mutate store state through List.
func TestListReturnsCopy(t *testing.T) {
	s, _ := Open(t.TempDir())
	if err := s.Add(mustProfile(t, "A", SourceManual, "", nil)); err != nil {
		t.Fatal(err)
	}
	got := s.List()
	got[0].Name = "mutated"
	again, ok := s.Get(got[0].ID)
	if !ok {
		t.Fatal("profile vanished")
	}
	if again.Name != "A" {
		t.Errorf("store mutated through List: name = %q", again.Name)
	}
}

// TestUpdatePreservesStableServerIDs models a subscription refresh: the same
// servers come back (renamed) and must keep their IDs, while a genuinely new
// server gets a new ID and a removed one disappears.
func TestUpdatePreservesStableServerIDs(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	original := sampleNodes()
	p := mustProfile(t, "Sub", SourceSubscription, "https://x.test/sub", original)
	if err := s.Add(p); err != nil {
		t.Fatal(err)
	}
	idByEndpoint := map[string]string{}
	for _, srv := range p.Servers {
		idByEndpoint[srv.Server] = srv.ID
	}

	// Refresh: node 0 and 1 unchanged but relabelled, node 2 dropped, a new
	// node added.
	refreshed := []model.Node{
		{Protocol: model.VLESS, Name: "Frankfurt #1 (renamed)", Server: "203.0.113.1", Port: 443, UUID: "uuid-a"},
		{Protocol: model.Hysteria2, Name: "AMS renamed", Server: "203.0.113.2", Port: 8443, Password: "pw-b"},
		{Protocol: model.Trojan, Name: "London", Server: "203.0.113.9", Port: 443, Password: "pw-new"},
	}
	updated := p
	updated.Servers = assignServerIDs(refreshed)
	if err := s.Update(updated); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, ok := s.Get(p.ID)
	if !ok {
		t.Fatal("profile gone after Update")
	}
	if len(got.Servers) != 3 {
		t.Fatalf("after refresh got %d servers, want 3", len(got.Servers))
	}

	byEndpoint := map[string]Server{}
	for _, srv := range got.Servers {
		byEndpoint[srv.Server] = srv
	}
	// Unchanged endpoints keep their IDs despite the rename.
	for _, ep := range []string{"203.0.113.1", "203.0.113.2"} {
		if byEndpoint[ep].ID != idByEndpoint[ep] {
			t.Errorf("server %s ID changed across refresh: %q -> %q", ep, idByEndpoint[ep], byEndpoint[ep].ID)
		}
	}
	// Dropped server is gone.
	if _, present := byEndpoint["203.0.113.3"]; present {
		t.Error("dropped server still present after refresh")
	}
	// New server has a fresh ID not seen before.
	newID := byEndpoint["203.0.113.9"].ID
	if newID == "" {
		t.Error("new server has no ID")
	}
	for _, old := range idByEndpoint {
		if newID == old {
			t.Errorf("new server reused an old ID %q", newID)
		}
	}

	// And it survives a reload.
	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	reloaded, _ := s2.Get(p.ID)
	if len(reloaded.Servers) != 3 {
		t.Errorf("reloaded servers = %d, want 3", len(reloaded.Servers))
	}
}

// TestServerIDChangesWhenServerChanges is the focused counterpart: mutate a
// server's credentials in place and confirm its ID changes after Update.
func TestServerIDChangesWhenServerChanges(t *testing.T) {
	s, _ := Open(t.TempDir())
	p := mustProfile(t, "Sub", SourceSubscription, "https://x.test/sub", sampleNodes()[:1])
	if err := s.Add(p); err != nil {
		t.Fatal(err)
	}
	oldID := p.Servers[0].ID

	changed := sampleNodes()[:1]
	changed[0].UUID = "rotated-uuid"
	upd := p
	upd.Servers = assignServerIDs(changed)
	if err := s.Update(upd); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(p.ID)
	if got.Servers[0].ID == oldID {
		t.Error("server ID should change when credentials change")
	}
}

func TestConcurrentAccess(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	const n = 20
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p := mustProfile(t, "P", SourceManual, "", sampleNodes()[:1])
			if err := s.Add(p); err != nil {
				t.Errorf("concurrent Add: %v", err)
				return
			}
			s.List()
			s.Get(p.ID)
		}()
	}
	wg.Wait()
	if got := s.List(); len(got) != n {
		t.Errorf("after %d concurrent adds, List = %d", n, len(got))
	}
}
