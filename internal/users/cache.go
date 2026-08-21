package users

import (
	"time"

	"github.com/yet-an-other/xform/internal/snapshot"
)

// Source produces user snapshots. *Collector is the production source.
type Source = snapshot.Source[Snapshot]

// Cache keeps the most recent user snapshot, refreshing it on an interval so
// HTTP handlers never wait on the stats API or the store.
type Cache = snapshot.Cache[Snapshot]

// NewCache creates a snapshot cache refreshed every interval.
func NewCache(source Source, interval time.Duration) *Cache {
	return snapshot.New(source, interval)
}
