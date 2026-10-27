package config

import (
	"context"
	"fmt"
	"os"
	"time"
)

type FileWatcher struct {
	Path     string
	Interval time.Duration // Defaults to 10s when nonpositive.
	Store    *Store
	// OnUpdate reports changed file bytes or read errors; a rejected update retains the last good config.
	OnUpdate func(err error)

	last []byte
}

// NeedLeaderElection is false so every replica keeps its configuration fresh.
func (w *FileWatcher) NeedLeaderElection() bool { return false }

func (w *FileWatcher) Start(ctx context.Context) error {
	if w.Store == nil || w.Path == "" {
		return fmt.Errorf("file watcher needs a store and a path")
	}
	interval := w.Interval
	if interval <= 0 {
		interval = 10 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			w.Poll()
		}
	}
}

func (w *FileWatcher) Poll() {
	data, err := os.ReadFile(w.Path)
	if err != nil {
		if w.OnUpdate != nil {
			w.OnUpdate(fmt.Errorf("read config file: %w", err))
		}
		return
	}
	if string(data) == string(w.last) {
		return
	}
	err = w.Store.Update(data)
	w.last = data
	if w.OnUpdate != nil {
		w.OnUpdate(err)
	}
}
