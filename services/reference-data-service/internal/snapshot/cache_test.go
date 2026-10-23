package snapshot

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/nexora-exchange/nexora/libs/go/refdata"
	"github.com/nexora-exchange/nexora/services/reference-data-service/internal/reference"
	"github.com/nexora-exchange/nexora/services/reference-data-service/internal/seed"
)

type switchSource struct {
	mu      sync.RWMutex
	active  reference.Active
	version reference.Version
}

func (s *switchSource) Active(context.Context, string) (reference.Active, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.active, nil
}

func (s *switchSource) GetVersion(context.Context, int64) (reference.Version, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.version, nil
}

func (s *switchSource) ActiveShards(context.Context) ([]string, error) {
	return []string{s.active.ShardID}, nil
}

func (s *switchSource) publish(version int64, displayName string, rollback bool) {
	document := seed.Document()
	document.Assets[0].DisplayName = displayName
	canonical, err := document.CanonicalBytes()
	if err != nil {
		panic(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.active = reference.Active{ShardID: reference.DefaultShard, Version: version, Rollback: rollback, ActivatedAt: time.Now().UTC()}
	s.version = reference.Version{
		Version: version, SchemaVersion: refdata.SchemaVersion, ContentHash: refdata.ContentHash(canonical),
		Document: canonical, Parsed: document, EffectiveAt: time.Now().UTC(),
		Canonicalization: "sha256/canonical-json-v1",
	}
}

func TestAtomicReplacementAndRollback(t *testing.T) {
	source := &switchSource{}
	source.publish(1, "Bitcoin v1", false)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cache := New(source, logger, nil, time.Hour, nil)
	first, err := cache.Reload(context.Background(), reference.DefaultShard)
	if err != nil {
		t.Fatalf("initial reload: %v", err)
	}
	if first.Version != 1 {
		t.Fatalf("unexpected initial version: %d", first.Version)
	}

	// Concurrent readers must never observe a mixed-version document. Every
	// version maps to a distinct display name.
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for index := 0; index < 8; index++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				current := cache.Current(reference.DefaultShard)
				if current == nil {
					t.Error("consumer observed a nil snapshot during replacement")
					return
				}
				switch current.Version {
				case 1:
					if current.Document.Assets[0].DisplayName != "Bitcoin v1" {
						t.Errorf("mixed version 1 snapshot: %q", current.Document.Assets[0].DisplayName)
						return
					}
				case 2:
					if current.Document.Assets[0].DisplayName != "Bitcoin v2" {
						t.Errorf("mixed version 2 snapshot: %q", current.Document.Assets[0].DisplayName)
						return
					}
				default:
					t.Errorf("unexpected version %d", current.Version)
					return
				}
			}
		}()
	}
	for index := 0; index < 50; index++ {
		if index%2 == 0 {
			source.publish(2, "Bitcoin v2", false)
		} else {
			source.publish(1, "Bitcoin v1", true)
		}
		if _, err := cache.Reload(context.Background(), reference.DefaultShard); err != nil {
			t.Fatalf("reload: %v", err)
		}
	}
	close(stop)
	wg.Wait()

	source.publish(2, "Bitcoin v2", false)
	if _, err := cache.Reload(context.Background(), reference.DefaultShard); err != nil {
		t.Fatalf("forward reload: %v", err)
	}
	start := time.Now()
	source.publish(1, "Bitcoin v1", true)
	rolledBack, err := cache.Reload(context.Background(), reference.DefaultShard)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("rollback reload: %v", err)
	}
	if rolledBack.Version != 1 || !rolledBack.Rollback || rolledBack.Document.Assets[0].DisplayName != "Bitcoin v1" {
		t.Fatalf("unexpected rollback snapshot: %+v", rolledBack)
	}
	if elapsed >= 60*time.Second {
		t.Fatalf("rollback exceeded 60s: %v", elapsed)
	}

	// A corrupted snapshot is rejected and the last valid snapshot remains.
	source.mu.Lock()
	source.version.ContentHash = "0000000000000000000000000000000000000000000000000000000000000000"
	source.mu.Unlock()
	if _, err := cache.Reload(context.Background(), reference.DefaultShard); err == nil {
		t.Fatal("corrupted snapshot was accepted")
	}
	current := cache.Current(reference.DefaultShard)
	if current == nil || current.Version != 1 || current.ContentHash != rolledBack.ContentHash {
		t.Fatalf("consumer lost the last valid snapshot: %+v", current)
	}
}

func TestReloadAllInstallsEveryActiveShard(t *testing.T) {
	source := &switchSource{}
	source.publish(4, "Bitcoin shard b", false)
	source.active.ShardID = "shard-b"
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cache := New(source, logger, nil, time.Hour, nil)
	shards, err := cache.ReloadAll(context.Background())
	if err != nil {
		t.Fatalf("reload all: %v", err)
	}
	if len(shards) != 1 || shards[0] != "shard-b" {
		t.Fatalf("unexpected reloaded shards: %v", shards)
	}
	if current := cache.Current("shard-b"); current == nil || current.Version != 4 {
		t.Fatalf("shard-b snapshot was not installed: %+v", current)
	}
}
