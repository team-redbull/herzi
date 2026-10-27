package config

import (
	"bytes"
	"sync"
	"sync/atomic"
)

// Store must be constructed with NewStore and must not be copied after first use.
type Store struct {
	current  atomic.Pointer[Config]
	valid    atomic.Bool
	updates  sync.Mutex
	lastGood []byte
	hasData  bool
}

// NewStore requires a config from Load and owns a copy of its pool map and override pointers.
func NewStore(initial Config) *Store {
	s := &Store{}
	initial.Pools = clonePools(initial.Pools)
	s.current.Store(&initial)
	s.valid.Store(true)
	return s
}

// Snapshot shares immutable pool maps and override pointers; old snapshots survive updates.
func (s *Store) Snapshot() Config { return *s.current.Load() }

// Valid reports whether the last Update succeeded, or true before the first attempt.
func (s *Store) Valid() bool { return s.valid.Load() }

// Update retains the previous snapshot on document, hub or default errors; pool errors are published.
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
