package postgres

import (
	"context"
	"encoding/hex"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nexora-exchange/nexora/libs/go/refdata"
	"github.com/nexora-exchange/nexora/services/reference-data-service/internal/reference"
	"github.com/nexora-exchange/nexora/services/reference-data-service/internal/seed"
	"github.com/nexora-exchange/nexora/services/reference-data-service/internal/snapshot"
)

func testDatabaseURL() string {
	if value := os.Getenv("NEXORA_TEST_DATABASE_URL"); value != "" {
		return value
	}
	return "postgres://identity@127.0.0.1:55432/identity_test?sslmode=disable"
}

func openIntegrationStore(t *testing.T) (*Store, string) {
	t.Helper()
	url := testDatabaseURL()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	store, err := New(ctx, url)
	if err != nil {
		t.Skipf("PostgreSQL unavailable at %s: %v", url, err)
	}
	t.Cleanup(store.Close)
	migrations := os.DirFS("../../migrations")
	// Start from a clean reference schema. This never touches identity tables.
	_ = Migrate(ctx, store.Pool(), migrations, "down")
	if err := Migrate(ctx, store.Pool(), migrations, "up"); err != nil {
		t.Fatalf("migrate up: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = Migrate(cleanupCtx, store.Pool(), migrations, "down")
	})
	return store, url
}

func newService(store *Store, clock func() time.Time) *reference.Service {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return reference.New(store, clock, nil, logger)
}

func TestMigrationsUpDown(t *testing.T) {
	store, _ := openIntegrationStore(t)
	ctx := t.Context()
	migrations := os.DirFS("../../migrations")
	var exists bool
	if err := store.Pool().QueryRow(ctx, `SELECT to_regclass('reference.versions') IS NOT NULL`).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatal("migration up did not create reference.versions")
	}
	if err := Migrate(ctx, store.Pool(), migrations, "down"); err != nil {
		t.Fatalf("migrate down: %v", err)
	}
	if err := store.Pool().QueryRow(ctx, `SELECT to_regclass('reference.versions') IS NULL`).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatal("migration down did not remove reference.versions")
	}
	if err := Migrate(ctx, store.Pool(), migrations, "up"); err != nil {
		t.Fatalf("migrate up after down: %v", err)
	}
}

func TestReferenceDataWorkflowPostgres(t *testing.T) {
	store, databaseURL := openIntegrationStore(t)
	ctx := t.Context()
	service := newService(store, nil)

	actorA := reference.Principal{UserID: uuid.New(), Roles: []string{"admin"}, Permissions: []string{reference.PermWrite}, RequestID: "test-a"}
	actorB := reference.Principal{UserID: uuid.New(), Roles: []string{"admin"}, Permissions: []string{reference.PermApprove}, RequestID: "test-b"}
	actorC := reference.Principal{UserID: uuid.New(), Roles: []string{"admin"}, Permissions: []string{reference.PermApprove}, RequestID: "test-c"}
	system := reference.Principal{Roles: []string{"system:seed"}, RequestID: "seed"}

	if err := service.Seed(ctx, seed.Document(), system); err != nil {
		t.Fatalf("seed: %v", err)
	}
	draft, err := service.GetDraft(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if draft.IsEmpty() || draft.ContentHash == "" || draft.Revision != 1 {
		t.Fatalf("unexpected seeded draft: revision=%d hash=%q", draft.Revision, draft.ContentHash)
	}
	seedCanonical, err := seed.Document().CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	if draft.ContentHash != refdata.ContentHash(seedCanonical) || string(draft.Canonical) != string(seedCanonical) {
		t.Fatal("seed draft is not canonical or hash does not match")
	}
	if err := service.Seed(ctx, seed.Document(), system); err != nil {
		t.Fatalf("idempotent seed: %v", err)
	}
	again, err := service.GetDraft(ctx)
	if err != nil || again.Revision != draft.Revision {
		t.Fatalf("seed was not idempotent: revision=%d err=%v", again.Revision, err)
	}

	invalid := seed.Document()
	invalid.Assets = append(invalid.Assets, invalid.Assets[0])
	invalidDraftValue, invalidErr := service.PutDraft(ctx, actorA, draft.Revision, nil, invalid)
	if invalidErr == nil {
		t.Fatal("invalid duplicate asset was accepted")
	}
	if typed := reference.AsError(invalidErr); typed.Code != reference.CodeValidationFailed {
		t.Fatalf("unexpected invalid-document error: %v", invalidErr)
	}
	if invalidDraftValue.Revision != 0 {
		t.Fatal("invalid draft was persisted")
	}

	proposal, err := service.SubmitProposal(ctx, actorA, draft.Revision)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if proposal.Status != reference.ProposalPending {
		t.Fatalf("unexpected proposal status: %s", proposal.Status)
	}
	if _, err := service.ApproveProposal(ctx, actorA, proposal.ProposalID, proposal.ContentHash, "self"); err == nil ||
		reference.AsError(err).Code != reference.CodeSelfApproval {
		t.Fatalf("self-approval was not rejected: %v", err)
	}
	if _, err := service.PublishProposal(ctx, actorA, proposal.ProposalID, time.Time{}); err == nil ||
		reference.AsError(err).Code != reference.CodeApprovalRequired {
		t.Fatalf("unapproved publication was not rejected: %v", err)
	}
	if _, err := service.ApproveProposal(ctx, actorB, proposal.ProposalID, proposal.ContentHash, "maker-checker"); err != nil {
		t.Fatalf("first approval: %v", err)
	}
	approved, err := service.ApproveProposal(ctx, actorC, proposal.ProposalID, proposal.ContentHash, "second approver")
	if err != nil {
		t.Fatalf("second approval: %v", err)
	}
	if approved.Status != reference.ProposalApproved || approved.ApprovalCount() != 2 {
		t.Fatalf("unexpected approved proposal: status=%s approvals=%d", approved.Status, approved.ApprovalCount())
	}
	versionOne, err := service.PublishProposal(ctx, actorA, proposal.ProposalID, time.Now().UTC())
	if err != nil {
		t.Fatalf("publish v1: %v", err)
	}
	if versionOne.Version != 1 || versionOne.ContentHash != draft.ContentHash {
		t.Fatalf("unexpected v1: %+v", versionOne.Summary())
	}
	if _, err := service.PublishProposal(ctx, actorA, proposal.ProposalID, time.Now().UTC()); err != nil {
		t.Fatalf("idempotent publish: %v", err)
	}
	versions, err := service.ListVersions(ctx, 10)
	if err != nil || len(versions) != 1 {
		t.Fatalf("idempotent publish created duplicate versions: %d %v", len(versions), err)
	}

	// Create v2 from v1 and prove a stale base is rejected.
	draft, err = service.GetDraft(ctx)
	if err != nil {
		t.Fatal(err)
	}
	updated := seed.Document()
	updated.Assets[0].DisplayName = "Bitcoin (fixture v2)"
	if _, err := service.PutDraft(ctx, actorA, draft.Revision, nil, updated); err == nil ||
		reference.AsError(err).Code != reference.CodeStaleVersion {
		t.Fatalf("draft based on no version was accepted after v1: %v", err)
	}
	base := versionOne.Version
	draft, err = service.PutDraft(ctx, actorA, draft.Revision, &base, updated)
	if err != nil {
		t.Fatalf("save v2 draft: %v", err)
	}
	proposalTwo, err := service.SubmitProposal(ctx, actorA, draft.Revision)
	if err != nil {
		t.Fatalf("submit v2: %v", err)
	}
	if _, err := service.ApproveProposal(ctx, actorB, proposalTwo.ProposalID, proposalTwo.ContentHash, "ok"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ApproveProposal(ctx, actorC, proposalTwo.ProposalID, proposalTwo.ContentHash, "ok"); err != nil {
		t.Fatal(err)
	}

	// Concurrent publication of one approved proposal must create exactly one
	// version and every caller must observe the same version and hash.
	type publishResult struct {
		version reference.Version
		err     error
	}
	results := make(chan publishResult, 8)
	for index := 0; index < 8; index++ {
		go func() {
			published, err := service.PublishProposal(ctx, actorA, proposalTwo.ProposalID, time.Now().UTC())
			results <- publishResult{version: published, err: err}
		}()
	}
	for index := 0; index < 8; index++ {
		result := <-results
		if result.err != nil {
			t.Fatalf("concurrent publish failed: %v", result.err)
		}
		if result.version.Version != 2 || result.version.ContentHash != proposalTwo.ContentHash {
			t.Fatalf("concurrent publish diverged: %+v", result.version.Summary())
		}
	}
	versions, err = service.ListVersions(ctx, 10)
	if err != nil || len(versions) != 2 {
		t.Fatalf("expected two versions, got %d (%v)", len(versions), err)
	}

	// Concurrent draft writes use optimistic concurrency: exactly one wins.
	current, err := service.GetDraft(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if current.BaseVersion == nil {
		t.Fatal("published draft has no base version")
	}
	draftResults := make(chan error, 2)
	for index := 0; index < 2; index++ {
		index := index
		go func() {
			doc := seed.Document()
			doc.Assets[0].DisplayName = "Concurrent " + string(rune('A'+index))
			_, err := service.PutDraft(ctx, actorA, current.Revision, current.BaseVersion, doc)
			draftResults <- err
		}()
	}
	successes, stale := 0, 0
	for index := 0; index < 2; index++ {
		err := <-draftResults
		switch {
		case err == nil:
			successes++
		case reference.AsError(err).Code == reference.CodeStaleDraft:
			stale++
		default:
			t.Fatalf("unexpected concurrent draft error: %v", err)
		}
	}
	if successes != 1 || stale != 1 {
		t.Fatalf("unexpected concurrent draft outcome: success=%d stale=%d", successes, stale)
	}

	// Initial activation must use the latest published version.
	cache := snapshot.New(store, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, 0, nil)
	if _, err := service.Activate(ctx, actorA, reference.ActivationRequest{
		ShardID: "shard-a", Version: 1, IdempotencyKey: "initial-old",
	}); err == nil || reference.AsError(err).Code != reference.CodeStaleVersion {
		t.Fatalf("initial stale activation was not rejected: %v", err)
	}
	activation, err := service.Activate(ctx, actorA, reference.ActivationRequest{
		ShardID: "shard-a", Version: 2, IdempotencyKey: "activate-v2",
	})
	if err != nil {
		t.Fatalf("activate v2: %v", err)
	}
	if activation.Status != reference.ActivationActive || activation.ShardID != "shard-a" {
		t.Fatalf("unexpected activation: %+v", activation)
	}
	activeSnapshot, err := cache.Reload(ctx, "shard-a")
	if err != nil {
		t.Fatalf("consumer reload: %v", err)
	}
	if activeSnapshot.Version != 2 || activeSnapshot.ContentHash != proposalTwo.ContentHash {
		t.Fatalf("unexpected active snapshot: %+v", activeSnapshot)
	}
	if _, err := service.Activate(ctx, actorA, reference.ActivationRequest{
		ShardID: "shard-a", Version: 2, IdempotencyKey: "activate-v2",
	}); err != nil {
		t.Fatalf("idempotent activation: %v", err)
	}
	if _, err := service.Activate(ctx, actorA, reference.ActivationRequest{
		ShardID: "shard-a", Version: 1, IdempotencyKey: "implicit-backward",
	}); err == nil || reference.AsError(err).Code != reference.CodeRollbackRequired {
		t.Fatalf("implicit backward activation was not rejected: %v", err)
	}
	if _, err := service.Activate(ctx, actorA, reference.ActivationRequest{
		ShardID: "shard-a", Version: 2, IdempotencyKey: "activate-v2", ExpectedVersion: int64Ptr(99),
	}); err != nil {
		// The idempotent key wins before expected-version validation only for the
		// same version. This call deliberately reuses the key with a stale
		// expectation and must not create another activation.
		t.Fatalf("idempotent replay should be safe: %v", err)
	}

	rollbackStart := time.Now()
	rollback, err := service.Rollback(ctx, actorA, reference.ActivationRequest{
		ShardID: "shard-a", Version: 1, IdempotencyKey: "rollback-v1", Reason: "verified regression",
		ExpectedVersion: int64Ptr(2),
	})
	rollbackElapsed := time.Since(rollbackStart)
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if rollback.Version != 1 || rollback.RollbackOf == nil || *rollback.RollbackOf != 2 {
		t.Fatalf("unexpected rollback activation: %+v", rollback)
	}
	if rollbackElapsed >= 60*time.Second {
		t.Fatalf("rollback exceeded 60s: %v", rollbackElapsed)
	}
	t.Logf("measured rollback activation duration: %s", rollbackElapsed)
	rolledBack, err := cache.Reload(ctx, "shard-a")
	if err != nil {
		t.Fatalf("rollback reload: %v", err)
	}
	if rolledBack.Version != 1 || !rolledBack.Rollback || rolledBack.ContentHash != versionOne.ContentHash {
		t.Fatalf("consumer did not atomically install rollback: %+v", rolledBack)
	}
	active, err := service.Active(ctx, "shard-a")
	if err != nil || active.Version != 1 || !active.Rollback {
		t.Fatalf("unexpected active record: %+v %v", active, err)
	}

	// Forward recovery after rollback is allowed only to the latest version.
	if _, err := service.Activate(ctx, actorA, reference.ActivationRequest{
		ShardID: "shard-a", Version: 2, IdempotencyKey: "recover-v2", ExpectedVersion: int64Ptr(1),
	}); err != nil {
		t.Fatalf("forward recovery: %v", err)
	}
	recovered, err := cache.Reload(ctx, "shard-a")
	if err != nil || recovered.Version != 2 || recovered.Rollback {
		t.Fatalf("unexpected recovery snapshot: %+v %v", recovered, err)
	}

	// Stale expected-version activation is rejected without changing active.
	if _, err := service.Activate(ctx, actorA, reference.ActivationRequest{
		ShardID: "shard-a", Version: 2, IdempotencyKey: "stale-expected", ExpectedVersion: int64Ptr(1),
	}); err == nil || reference.AsError(err).Code != reference.CodeStaleVersion {
		t.Fatalf("stale expected version was not rejected: %v", err)
	}

	// Audit chain integrity: each newer event links to the previous event hash.
	events, err := service.ListAudit(ctx, 200, time.Time{})
	if err != nil || len(events) < 10 {
		t.Fatalf("insufficient audit events: %d %v", len(events), err)
	}
	for index := 0; index < len(events); index++ {
		currentEvent := events[index]
		prevBytes, err := hex.DecodeString(currentEvent.PrevHash)
		if err != nil {
			t.Fatal(err)
		}
		expected := hex.EncodeToString(auditDigest(prevBytes, currentEvent, currentEvent.ActorRole))
		if expected != currentEvent.EventHash {
			t.Fatalf("audit event hash mismatch at index %d", index)
		}
		if index < len(events)-1 && currentEvent.PrevHash != events[index+1].EventHash {
			t.Fatalf("audit chain break at index %d", index)
		}
	}

	// Restart recovery: a fresh pool and consumer rebuild from PostgreSQL.
	restartStore, err := New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("restart store: %v", err)
	}
	defer restartStore.Close()
	restartService := newService(restartStore, nil)
	restartActive, err := restartService.Active(ctx, "shard-a")
	if err != nil || restartActive.Version != 2 {
		t.Fatalf("restart active recovery: %+v %v", restartActive, err)
	}
	restartCache := snapshot.New(restartStore, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, 0, nil)
	restartSnapshot, err := restartCache.Reload(ctx, "shard-a")
	if err != nil || restartSnapshot.Version != 2 || restartSnapshot.ContentHash != proposalTwo.ContentHash {
		t.Fatalf("restart snapshot recovery: %+v %v", restartSnapshot, err)
	}

	// Scheduled activation is promoted exactly once and idempotently.
	future := time.Now().UTC().Add(150 * time.Millisecond)
	scheduled, err := service.Activate(ctx, actorA, reference.ActivationRequest{
		ShardID: "shard-b", Version: 2, IdempotencyKey: "scheduled-v2", EffectiveAt: future,
	})
	if err != nil {
		t.Fatalf("schedule activation: %v", err)
	}
	if scheduled.Status != reference.ActivationPending {
		t.Fatalf("expected pending scheduled activation: %+v", scheduled)
	}
	time.Sleep(200 * time.Millisecond)
	promoted, err := service.ActivateDue(ctx)
	if err != nil {
		t.Fatalf("promote due: %v", err)
	}
	if len(promoted) != 1 || promoted[0].Status != reference.ActivationActive {
		t.Fatalf("scheduled activation was not promoted: %+v", promoted)
	}
	promotedAgain, err := service.ActivateDue(ctx)
	if err != nil {
		t.Fatalf("second promote due: %v", err)
	}
	if len(promotedAgain) != 0 {
		t.Fatalf("scheduled activation was promoted twice: %+v", promotedAgain)
	}
	activeScheduled, err := service.Active(ctx, "shard-b")
	if err != nil || activeScheduled.Version != 2 {
		t.Fatalf("scheduled active recovery: %+v %v", activeScheduled, err)
	}

	// Immutability is enforced by PostgreSQL triggers, not just Go code.
	if _, err := store.Pool().Exec(ctx, `UPDATE reference.versions SET content_hash=content_hash WHERE version=1`); err == nil {
		t.Fatal("published version update was not rejected by the database")
	}
	if _, err := store.Pool().Exec(ctx, `DELETE FROM reference.versions WHERE version=1`); err == nil {
		t.Fatal("published version delete was not rejected by the database")
	}
	if _, err := store.Pool().Exec(ctx, `UPDATE reference.approvals SET reason='tampered'`); err == nil {
		t.Fatal("approval update was not rejected by the database")
	}
	if _, err := store.Pool().Exec(ctx, `DELETE FROM reference.audit_events`); err == nil {
		t.Fatal("audit delete was not rejected by the database")
	}
	if _, err := store.Pool().Exec(ctx, `INSERT INTO reference.approvals
		(proposal_id, approver_id, decision, reason, proposal_hash, decided_at)
		VALUES ($1,$2,'approved','self',$3,now())`, proposal.ProposalID, actorA.UserID, proposal.ContentHash); err == nil {
		t.Fatal("database accepted a self-approval bypassing the service")
	}
}

func int64Ptr(value int64) *int64 { return &value }
