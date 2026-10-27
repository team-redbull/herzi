package config_test

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/team-redbull/herzi/internal/config"
)

// Part 3, Edge cases: "a bad update is rejected"; Part 2: one frozen snapshot per pass.
func TestStoreLastGoodAndValid(t *testing.T) {
	initial := validConfig(t)
	s := config.NewStore(initial)
	if !s.Valid() {
		t.Fatal("initial store must be valid")
	}
	if diff := cmp.Diff(initial, s.Snapshot()); diff != "" {
		t.Fatal(diff)
	}
	good := []byte(minimal + "pools: {test: {minFree: 5}}\n")
	if err := s.Update(good); err != nil {
		t.Fatal(err)
	}
	old := s.Snapshot()
	if !old.Manages("test") || old.ForPool("test").MinFree != 5 || !s.Valid() {
		t.Fatal("successful update not published")
	}
	// Syntax, strictness, and semantic failures all keep the last good value.
	for _, bad := range []string{"[", minimal + "typo: true\n", minimal + "defaults: {minFree: -1}\n"} {
		if err := s.Update([]byte(bad)); err == nil {
			t.Fatal("bad update accepted")
		}
		if s.Valid() {
			t.Fatal("bad update must clear Valid")
		}
		if diff := cmp.Diff(old, s.Snapshot()); diff != "" {
			t.Fatal(diff)
		}
		if err := s.Update(good); err != nil {
			t.Fatal(err)
		}
		if !s.Valid() {
			t.Fatal("unchanged good bytes must restore Valid")
		}
		if s.Snapshot().Pools["test"].MinFree != old.Pools["test"].MinFree {
			t.Fatal("unchanged bytes must be a no-op")
		}
	}
	// Update owns its comparison bytes; caller changes cannot poison the no-op cache.
	good[len(good)-4] = '9'
	if err := s.Update(good); err != nil {
		t.Fatal(err)
	}
	if old.ForPool("test").MinFree != 5 {
		t.Fatal("old snapshot was not frozen")
	}
	if s.Snapshot().ForPool("test").MinFree != 9 {
		t.Fatal("input bytes were not copied")
	}
}

func TestStoreOwnsInitial(t *testing.T) {
	initial := validConfig(t)
	initial.Pools = map[string]config.PoolOverride{"test": {
		HighPct: new(30), MinFree: new(2), MaxOpenRunsPerPool: new(15),
		ProbeInterval: ptr(20 * time.Minute), DrawGrace: ptr(2 * time.Minute), FailureMemory: ptr(6 * time.Hour), SettlingMax: ptr(time.Hour),
		DryRunGrow: new(false), Paused: new(false),
	}, "empty": {}}
	s := config.NewStore(initial)
	*initial.Pools["test"].MinFree = 999
	*initial.Pools["test"].HighPct = 999
	delete(initial.Pools, "empty")
	if s.Snapshot().ForPool("test").HighPct != 30 || s.Snapshot().ForPool("test").MinFree != 2 || !s.Snapshot().Manages("empty") {
		t.Fatal("initial config aliases store")
	}
}

func TestStoreConcurrentSnapshotUpdate(t *testing.T) {
	s := config.NewStore(validConfig(t))
	const attempts = 60
	var wg sync.WaitGroup
	for worker := range 8 {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := range attempts {
				if worker%2 == 0 {
					free := i % 10
					data := fmt.Appendf(nil, "%sdefaults: {minFree: %d}\npools: {test: {minFree: %d}}\n", minimal, free, free)
					if err := s.Update(data); err != nil {
						t.Error(err)
					}
					if i%7 == 0 && s.Update([]byte("invalid")) == nil {
						t.Error("invalid update accepted")
					}
				} else {
					c := s.Snapshot()
					if err := c.Validate(); err != nil {
						t.Error(err)
					}
					if c.Manages("test") && c.ForPool("test").MinFree != c.Defaults.MinFree {
						t.Error("mixed configuration generations")
					}
					_ = s.Valid()
				}
			}
		}(worker)
	}
	wg.Wait()
	if err := s.Update([]byte(minimal)); err != nil {
		t.Fatal(err)
	}
	if !s.Valid() {
		t.Fatal("final successful update must be valid")
	}
}

func TestRemovedKeysKeepLastGoodConfig(t *testing.T) {
	s := config.NewStore(validConfig(t))
	good := []byte(minimal + "pools: {test: {minFree: 5}}\n")
	if err := s.Update(good); err != nil {
		t.Fatal(err)
	}
	before := s.Snapshot()
	for _, suffix := range []string{"defaults: {lowPct: null}", "defaults: {minBand: 0}", "pools: {test: {lowPct: 15}}", "pools: {test: {minBand: null}}"} {
		requireError(t, s.Update([]byte(minimal+suffix)), "was removed")
		if s.Valid() {
			t.Fatal("obsolete document accepted")
		}
		if diff := cmp.Diff(before, s.Snapshot()); diff != "" {
			t.Fatal(diff)
		}
	}
	if err := s.Update(good); err != nil {
		t.Fatal(err)
	}
	if !s.Valid() {
		t.Fatal("last-good retry rejected")
	}
}
