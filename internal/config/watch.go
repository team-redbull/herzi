package config

import (
	"context"
	"fmt"
	"os"
	"time"
)

// FileWatcher keeps a Store in step with a config file. A ConfigMap mounted as a volume is updated in
// place by the kubelet (within about a minute), so polling the file is enough: no ConfigMap RBAC and no
// second informer. It is a controller-runtime Runnable (Start blocks until ctx is done).
type FileWatcher struct {
	Path     string
	Interval time.Duration // default 10s
	Store    *Store
	// OnUpdate is called after every poll that read DIFFERENT bytes than the last good config
	// (err == nil: the new config is live; err != nil: it was rejected and the last good one stays).
	OnUpdate func(err error)

	last []byte
}

// NeedLeaderElection returns false: every replica must keep its own config fresh.
func (w *FileWatcher) NeedLeaderElection() bool { return false }

// Start polls until ctx is cancelled.
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

// Poll reads the file once. A read error is reported through OnUpdate and keeps the last good config.
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
