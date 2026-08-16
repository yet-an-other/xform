package xraystatus

import (
	"time"

	"github.com/yet-an-other/xform/internal/snapshot"
)

// Source produces xray status snapshots. *Collector is the production source.
type Source = snapshot.Source[Status]

// Cache keeps the most recent xray status snapshot, refreshing it on an
// interval so HTTP handlers never wait on D-Bus or the xray binary.
type Cache = snapshot.Cache[Status]

// NewCache creates a snapshot cache refreshed every interval.
func NewCache(source Source, interval time.Duration) *Cache {
	return snapshot.New(source, interval)
}
