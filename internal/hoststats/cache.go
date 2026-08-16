package hoststats

import (
	"time"

	"github.com/yet-an-other/xform/internal/snapshot"
)

// Source produces host stat snapshots. *Collector is the production source.
type Source = snapshot.Source[Stats]

// Cache keeps the most recent host stats snapshot, refreshing it on an
// interval so HTTP handlers never wait on operating system reads.
type Cache = snapshot.Cache[Stats]

// NewCache creates a snapshot cache refreshed every interval.
func NewCache(source Source, interval time.Duration) *Cache {
	return snapshot.New(source, interval)
}
