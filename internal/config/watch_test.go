package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFileWatcherAppliesAndRejects(t *testing.T) {
	good, err := os.ReadFile("testdata/sample.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(good)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, good, 0o600); err != nil {
		t.Fatal(err)
	}
	store := NewStore(cfg)
	var calls []error
	w := &FileWatcher{Path: path, Store: store, OnUpdate: func(err error) { calls = append(calls, err) }}

	w.Poll()
	if len(calls) != 1 || calls[0] != nil || !store.Valid() {
		t.Fatalf("first poll: calls=%v valid=%v", calls, store.Valid())
	}
	w.Poll()
	if len(calls) != 1 {
		t.Fatalf("an unchanged file must not call OnUpdate again: %v", calls)
	}

	if err := os.WriteFile(path, []byte("hubName: ocp4-x\nbogusKey: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	w.Poll()
	if len(calls) != 2 || calls[1] == nil || store.Valid() {
		t.Fatalf("a bad file must be rejected and reported: calls=%v valid=%v", calls, store.Valid())
	}
	if store.Snapshot().HubName != cfg.HubName {
		t.Fatal("a rejected update must keep the last good config")
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	w.Poll()
	if len(calls) != 3 || calls[2] == nil {
		t.Fatalf("a missing file must be reported: %v", calls)
	}
}

func TestFileWatcherNeedsAStoreAndAPath(t *testing.T) {
	if err := (&FileWatcher{}).Start(t.Context()); err == nil {
		t.Fatal("expected an error")
	}
	if (&FileWatcher{}).NeedLeaderElection() {
		t.Fatal("every replica must watch its own config")
	}
}
