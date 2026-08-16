package hoststats

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Source produces host stat snapshots. *Collector is the production source.
type Source interface {
	Collect(context.Context) (Stats, error)
}

// Cache keeps the most recent host stats snapshot, refreshing it on an
// interval so HTTP handlers never wait on operating system reads.
type Cache struct {
	source   Source
	interval time.Duration

	refreshMu sync.Mutex
	mu        sync.RWMutex
	latest    Stats
	ok        bool
}

// NewCache creates a snapshot cache refreshed every interval.
func NewCache(source Source, interval time.Duration) *Cache {
	return &Cache{source: source, interval: interval}
}

// Latest returns the most recent snapshot, collecting one on demand when the
// cache has not been primed yet. Once primed, refresh failures keep serving
// the last good snapshot.
func (c *Cache) Latest(ctx context.Context) (Stats, error) {
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
func (c *Cache) Start(ctx context.Context) {
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
					slog.Warn("refresh host stats", "error", err)
				}
			}
		}
	}()
}

// collect gathers a fresh snapshot. Callers hold refreshMu. A failed collect
// keeps and returns the last good snapshot when one exists.
func (c *Cache) collect(ctx context.Context) (Stats, error) {
	stats, err := c.source.Collect(ctx)

	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil {
		if c.ok {
			return c.latest, nil
		}
		return Stats{}, err
	}
	c.latest = stats
	c.ok = true
	return c.latest, nil
}
