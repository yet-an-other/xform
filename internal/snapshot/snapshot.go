// Package snapshot provides a periodically refreshed cache of a collected
// snapshot — the shared pattern behind the panel's collectors (host stats,
// xray status). HTTP handlers read Latest; a background loop keeps it fresh.
package snapshot

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Source produces snapshots of T.
type Source[T any] interface {
	Collect(context.Context) (T, error)
}

// Cache keeps the most recent snapshot, refreshing it on an interval so HTTP
// handlers never wait on operating system reads.
type Cache[T any] struct {
	source   Source[T]
	interval time.Duration

	refreshMu sync.Mutex
	mu        sync.RWMutex
	latest    T
	ok        bool
}

// New creates a snapshot cache refreshed every interval.
func New[T any](source Source[T], interval time.Duration) *Cache[T] {
	return &Cache[T]{source: source, interval: interval}
}

// Latest returns the most recent snapshot, collecting one on demand when the
// cache has not been primed yet. Once primed, refresh failures keep serving
// the last good snapshot.
func (c *Cache[T]) Latest(ctx context.Context) (T, error) {
	c.mu.RLock()
	if c.ok {
		defer c.mu.RUnlock()
		return c.latest, nil
	}
	c.mu.RUnlock()

	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	c.mu.RLock()
	if c.ok { // primed while waiting for the refresh lock
		defer c.mu.RUnlock()
		return c.latest, nil
	}
	c.mu.RUnlock()
	return c.collect(ctx)
}

// Start refreshes the snapshot every interval until the context is cancelled.
func (c *Cache[T]) Start(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(c.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				c.refreshMu.Lock()
				_, err := c.collect(ctx)
				c.refreshMu.Unlock()
				if err != nil {
					slog.Warn("refresh snapshot", "error", err)
				}
			}
		}
	}()
}

// collect gathers a fresh snapshot. Callers hold refreshMu. A failed collect
// keeps and returns the last good snapshot when one exists.
func (c *Cache[T]) collect(ctx context.Context) (T, error) {
	snap, err := c.source.Collect(ctx)

	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil {
		if c.ok {
			return c.latest, nil
		}
		var zero T
		return zero, err
	}
	c.latest = snap
	c.ok = true
	return c.latest, nil
}
