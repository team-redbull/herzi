package config

import (
	"bytes"
	"sync"
	"sync/atomic"
)

// Store implements Part 3, Edge cases: "a bad update is rejected" and Part 2's
// invariant: "One frozen config ... per reconcile". Construct it with NewStore.
// Store must not be copied after first use.
type Store struct {
	current  atomic.Pointer[Config]
	valid    atomic.Bool
	updates  sync.Mutex
	lastGood []byte
	hasData  bool
}

// NewStore installs a config produced by Load (including pool-local semantic errors).
// It owns a copy of the initial map and pointers, so later caller edits are isolated.
// Valid starts true, before any Update attempt has failed.
func NewStore(initial Config) *Store {
	s := &Store{}
	initial.Pools = clonePools(initial.Pools)
	s.current.Store(&initial)
	s.valid.Store(true)
	return s
}

// Snapshot returns a cheap, atomic snapshot for a complete reconcile pass.
// Treat its Pools map and override pointers as immutable; use ForPool for value copies.
// Old snapshots remain valid after Update publishes a new config.
func (s *Store) Snapshot() Config { return *s.current.Load() }

// Valid reports whether the last Update succeeded (true initially).
func (s *Store) Valid() bool { return s.valid.Load() }

// Update publishes a document-valid config, retaining pool-local semantic errors.
// Document, hub and default errors keep the previous snapshot.
// Concurrent updates are serialized, including the unchanged-bytes success path.
func (s *Store) Update(data []byte) error {
	s.updates.Lock()
	defer s.updates.Unlock()
	if s.hasData && bytes.Equal(data, s.lastGood) {
		s.valid.Store(true)
		return nil
	}
	c, err := Load(data)
	if err != nil {
		s.valid.Store(false)
		return err
	}
	s.current.Store(&c)
	s.lastGood = bytes.Clone(data)
	s.hasData = true
	s.valid.Store(true)
	return nil
}

func clonePointer[T any](p *T) *T {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

func clonePools(pools map[string]PoolOverride) map[string]PoolOverride {
	if pools == nil {
		return nil
	}
	out := make(map[string]PoolOverride, len(pools))
	for name, p := range pools {
		p.HighPct = clonePointer(p.HighPct)
		p.MinFree = clonePointer(p.MinFree)
		p.MaxOpenRunsPerPool = clonePointer(p.MaxOpenRunsPerPool)
		p.ProbeInterval = clonePointer(p.ProbeInterval)
		p.DrawGrace = clonePointer(p.DrawGrace)
		p.FailureMemory = clonePointer(p.FailureMemory)
		p.SettlingMax = clonePointer(p.SettlingMax)
		p.DryRunGrow = clonePointer(p.DryRunGrow)
		p.Paused = clonePointer(p.Paused)
		out[name] = p
	}
	return out
}
