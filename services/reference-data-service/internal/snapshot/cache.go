// Package snapshot builds and atomically installs complete reference-data
// snapshots for local consumers. PostgreSQL remains authoritative; the
// in-memory consumer is a read-through cache of one verified snapshot.
package snapshot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/nexora-exchange/nexora/libs/go/refdata"
	"github.com/nexora-exchange/nexora/services/reference-data-service/internal/reference"
)

// Source is the authoritative persistence contract needed by the cache.
type Source interface {
	Active(ctx context.Context, shardID string) (reference.Active, error)
	GetVersion(ctx context.Context, version int64) (reference.Version, error)
	ActiveShards(ctx context.Context) ([]string, error)
}

// Observer receives cache signals. It must not block.
type Observer interface {
	SnapshotApplied(snapshot refdata.Snapshot)
	SnapshotRejected(reason string)
	ObserveSnapshot(snapshot refdata.Snapshot, now time.Time, staleAfter time.Duration)
	Divergence()
}

// Cache installs verified snapshots atomically per shard.
type Cache struct {
	source     Source
	logger     *slog.Logger
	clock      func() time.Time
	staleAfter time.Duration
	observer   Observer

	mu        sync.RWMutex
	consumers map[string]*refdata.Consumer
}

// New constructs a cache.
func New(source Source, logger *slog.Logger, clock func() time.Time, staleAfter time.Duration, observer Observer) *Cache {
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Cache{
		source: source, logger: logger, clock: clock, staleAfter: staleAfter, observer: observer,
		consumers: map[string]*refdata.Consumer{},
	}
}

// Build verifies and returns a complete snapshot from authoritative records.
func Build(active reference.Active, version reference.Version) (refdata.Snapshot, error) {
	if active.Version != version.Version {
		return refdata.Snapshot{}, fmt.Errorf("active version %d does not match published version %d", active.Version, version.Version)
	}
	if len(version.Document) == 0 {
		return refdata.Snapshot{}, errors.New("published version has no document")
	}
	if err := refdata.VerifyHash(version.Document, version.ContentHash); err != nil {
		return refdata.Snapshot{}, fmt.Errorf("published version hash verification failed: %w", err)
	}
	document, err := refdata.Parse(version.Document)
	if err != nil {
		return refdata.Snapshot{}, err
	}
	if document.SchemaVersion != version.SchemaVersion {
		return refdata.Snapshot{}, refdata.ErrSchema
	}
	if issues := document.Validate(); len(issues) > 0 {
		return refdata.Snapshot{}, &refdata.ValidationError{Issues: issues}
	}
	return refdata.Snapshot{
		Version:       version.Version,
		ContentHash:   version.ContentHash,
		SchemaVersion: version.SchemaVersion,
		EffectiveAt:   version.EffectiveAt,
		ShardID:       active.ShardID,
		Rollback:      active.Rollback,
		Document:      document,
		Canonical:     version.Document,
	}, nil
}

// Reload loads the active version and atomically installs it. On any failure the
// previously installed snapshot remains effective.
func (c *Cache) Reload(ctx context.Context, shardID string) (refdata.Snapshot, error) {
	if shardID == "" {
		shardID = reference.DefaultShard
	}
	active, err := c.source.Active(ctx, shardID)
	if err != nil {
		return refdata.Snapshot{}, err
	}
	version, err := c.source.GetVersion(ctx, active.Version)
	if err != nil {
		return refdata.Snapshot{}, err
	}
	next, err := Build(active, version)
	if err != nil {
		if c.observer != nil {
			c.observer.SnapshotRejected("build")
		}
		return refdata.Snapshot{}, err
	}
	consumer := c.consumer(shardID)
	if current := consumer.Current(); current != nil && current.Version > next.Version && !active.Rollback {
		if c.observer != nil {
			c.observer.Divergence()
		}
		return refdata.Snapshot{}, refdata.ErrStaleVersion
	}
	if err := consumer.Apply(next, refdata.ApplyOptions{AllowRollback: active.Rollback}); err != nil {
		return refdata.Snapshot{}, err
	}
	if c.observer != nil {
		c.observer.ObserveSnapshot(next, c.clock(), c.staleAfter)
	}
	c.logger.InfoContext(ctx, "reference-data snapshot active",
		"shard_id", shardID, "version", next.Version, "content_hash", next.ContentHash, "rollback", next.Rollback)
	return next, nil
}

// Current returns the installed snapshot for a shard.
func (c *Cache) Current(shardID string) *refdata.Snapshot {
	c.mu.RLock()
	consumer := c.consumers[shardID]
	c.mu.RUnlock()
	if consumer == nil {
		return nil
	}
	return consumer.Current()
}

// ReloadAll loads every shard that has an active configuration. A failure for
// one shard does not prevent other shards from being installed; the first
// failure is returned so callers can surface partial recovery.
func (c *Cache) ReloadAll(ctx context.Context) ([]string, error) {
	shards, err := c.source.ActiveShards(ctx)
	if err != nil {
		return nil, err
	}
	loaded := make([]string, 0, len(shards))
	var firstErr error
	for _, shard := range shards {
		if _, err := c.Reload(ctx, shard); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		loaded = append(loaded, shard)
	}
	return loaded, firstErr
}

// Run periodically reloads every shard with an active configuration.
func (c *Cache) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.reloadAll(ctx)
		}
	}
}

func (c *Cache) reloadAll(ctx context.Context) {
	shards, err := c.source.ActiveShards(ctx)
	if err != nil {
		c.logger.ErrorContext(ctx, "reference-data shard discovery failed", "error", err)
		return
	}
	if len(shards) == 0 {
		return
	}
	for _, shard := range shards {
		reloadCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		_, err := c.Reload(reloadCtx, shard)
		cancel()
		if err != nil {
			c.logger.WarnContext(ctx, "reference-data reload retained previous snapshot",
				"shard_id", shard, "error", err)
		}
	}
}

func (c *Cache) consumer(shardID string) *refdata.Consumer {
	c.mu.RLock()
	consumer := c.consumers[shardID]
	c.mu.RUnlock()
	if consumer != nil {
		return consumer
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if consumer = c.consumers[shardID]; consumer != nil {
		return consumer
	}
	consumer = refdata.NewConsumer(c.observer)
	c.consumers[shardID] = consumer
	return consumer
}
