package refdata

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// Observer receives consumer lifecycle events for metrics and logging. It must
// not block; implementations should update counters or emit a log line.
type Observer interface {
	SnapshotApplied(snapshot Snapshot)
	SnapshotRejected(reason string)
}

// Consumer holds one complete immutable snapshot. Replacement is atomic: readers
// see either the previous snapshot or the next one, never a partial document.
type Consumer struct {
	current  atomic.Pointer[Snapshot]
	mu       sync.Mutex
	observer Observer
	applied  atomic.Uint64
	rejected atomic.Uint64
}

// NewConsumer constructs an empty consumer.
func NewConsumer(observer Observer) *Consumer {
	return &Consumer{observer: observer}
}

// Apply verifies and installs a snapshot.
//
// Version regression is rejected unless options.AllowRollback is true and the
// snapshot for an explicit rollback. Hash and schema verification always run.
func (c *Consumer) Apply(snapshot Snapshot, options ApplyOptions) error {
	canonical := snapshot.Canonical
	if len(canonical) == 0 {
		encoded, err := snapshot.Document.CanonicalBytes()
		if err != nil {
			return err
		}
		canonical = encoded
	}
	if snapshot.ContentHash == "" {
		return fmt.Errorf("reference-data snapshot is missing a content hash")
	}
	if err := VerifyHash(canonical, snapshot.ContentHash); err != nil {
		c.reject("hash_mismatch")
		return err
	}
	parsed, err := Parse(canonical)
	if err != nil {
		c.reject("decode")
		return err
	}
	if parsed.SchemaVersion != SchemaVersion {
		c.reject("schema")
		return ErrSchema
	}
	if issues := parsed.Validate(); len(issues) > 0 {
		c.reject("validation")
		return &ValidationError{Issues: issues}
	}
	if snapshot.Version <= 0 {
		c.reject("version")
		return fmt.Errorf("reference-data snapshot version must be positive")
	}

	next := snapshot
	next.Document = parsed
	next.Canonical = canonical
	next.SchemaVersion = parsed.SchemaVersion

	c.mu.Lock()
	defer c.mu.Unlock()
	current := c.current.Load()
	if current != nil {
		if next.Version < current.Version && !options.AllowRollback {
			c.reject("stale_version")
			return ErrStaleVersion
		}
		if next.Version == current.Version {
			if next.ContentHash != current.ContentHash {
				c.reject("version_hash_conflict")
				return fmt.Errorf("reference-data version %d has conflicting content hashes", next.Version)
			}
			return nil
		}
		if next.Version > current.Version && options.AllowRollback {
			// A normal forward update is always allowed; the flag only grants
			// permission for a regression.
		}
	}
	c.current.Store(&next)
	c.applied.Add(1)
	if c.observer != nil {
		c.observer.SnapshotApplied(next)
	}
	return nil
}

// Current returns the installed snapshot, or nil when none is loaded.
func (c *Consumer) Current() *Snapshot {
	return c.current.Load()
}

// Stats returns applied and rejected counters.
func (c *Consumer) Stats() (applied, rejected uint64) {
	return c.applied.Load(), c.rejected.Load()
}

// Age reports the age of the current snapshot.
func (c *Consumer) Age(now time.Time) (time.Duration, bool) {
	current := c.current.Load()
	if current == nil {
		return 0, false
	}
	return current.Age(now), true
}

func (c *Consumer) reject(reason string) {
	c.rejected.Add(1)
	if c.observer != nil {
		c.observer.SnapshotRejected(reason)
	}
}

// ApplyOptions controls rollback acceptance.
type ApplyOptions struct {
	// AllowRollback permits an explicit decrease to an older verified version.
	AllowRollback bool
}
