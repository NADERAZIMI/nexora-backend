package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nexora-exchange/nexora/libs/go/observability"
	"github.com/nexora-exchange/nexora/libs/go/refdata"
	"github.com/nexora-exchange/nexora/services/reference-data-service/internal/auth"
	"github.com/nexora-exchange/nexora/services/reference-data-service/internal/reference"
	"github.com/nexora-exchange/nexora/services/reference-data-service/internal/seed"
	"github.com/nexora-exchange/nexora/services/reference-data-service/internal/snapshot"
	"github.com/prometheus/client_golang/prometheus"
)

type fakeAuthorizer struct {
	principal reference.Principal
	err       error
	calls     []authCall
}

type authCall struct {
	token, permission, stepUpAction, requestID string
}

func (f *fakeAuthorizer) Authorize(_ context.Context, token, permission, stepUpAction, requestID string) (reference.Principal, error) {
	f.calls = append(f.calls, authCall{token, permission, stepUpAction, requestID})
	if f.err != nil {
		return reference.Principal{}, f.err
	}
	return f.principal, nil
}

type stubStore struct {
	reference.Store
	draft reference.Draft
	saved reference.Draft
}

func (s *stubStore) LoadDraft(context.Context) (reference.Draft, error) { return s.draft, nil }

func (s *stubStore) SaveDraft(_ context.Context, request reference.SaveDraftRequest) (reference.Draft, error) {
	document, err := refdata.Parse(request.Document)
	if err != nil {
		return reference.Draft{}, err
	}
	s.saved = reference.Draft{
		Revision: request.ExpectedRevision + 1, BaseVersion: request.BaseVersion,
		ContentHash: request.ContentHash, Document: document, Canonical: request.Document,
	}
	return s.saved, nil
}

func (s *stubStore) Ping(context.Context) error { return nil }

type stubSource struct {
	active  reference.Active
	version reference.Version
}

func (s *stubSource) Active(context.Context, string) (reference.Active, error) { return s.active, nil }
func (s *stubSource) GetVersion(context.Context, int64) (reference.Version, error) {
	return s.version, nil
}
func (s *stubSource) ActiveShards(context.Context) ([]string, error) {
	return []string{s.active.ShardID}, nil
}

func newTestServer(t *testing.T, authorizer auth.Authorizer, store *stubStore) http.Handler {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	service := reference.New(store, nil, nil, logger)
	document := seed.Document()
	canonical, err := document.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	source := &stubSource{
		active: reference.Active{ShardID: reference.DefaultShard, Version: 3, ActivatedAt: time.Now().UTC()},
		version: reference.Version{
			Version: 3, SchemaVersion: refdata.SchemaVersion, ContentHash: refdata.ContentHash(canonical),
			Document: canonical, Parsed: document, EffectiveAt: time.Now().UTC(),
			Canonicalization: "sha256/canonical-json-v1",
		},
	}
	cache := snapshot.New(source, logger, nil, time.Hour, nil)
	providers := &observability.Providers{Registry: prometheus.NewRegistry()}
	return New(service, authorizer, cache, store, logger, "test", "internal-test-token", nil, providers)
}

func TestProtectedRoutesRequireLiveIdentityDecision(t *testing.T) {
	store := &stubStore{}
	authorizer := &fakeAuthorizer{principal: reference.Principal{UserID: uuid.New(), RequestID: "req"}}
	handler := newTestServer(t, authorizer, store)

	request := httptest.NewRequest(http.MethodGet, "/v1/admin/reference-data/draft", nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("missing token status = %d, want 401", recorder.Code)
	}
	if len(authorizer.calls) != 0 {
		t.Fatal("identity was called without a token")
	}

	authorizer.err = reference.Forbidden("denied")
	request = httptest.NewRequest(http.MethodGet, "/v1/admin/reference-data/draft", nil)
	request.Header.Set("Authorization", "Bearer session-token")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("denied status = %d, want 403", recorder.Code)
	}

	authorizer.err = reference.NewError(403, reference.CodeStepUpRequired, "step-up required", nil)
	request = httptest.NewRequest(http.MethodPut, "/v1/admin/reference-data/draft",
		strings.NewReader(`{"expected_revision":0,"document":{}}`))
	request.Header.Set("Authorization", "Bearer session-token")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("step-up status = %d, want 403", recorder.Code)
	}
	last := authorizer.calls[len(authorizer.calls)-1]
	if last.permission != reference.PermWrite || last.stepUpAction != reference.StepUpWrite {
		t.Fatalf("unexpected authorization request: %+v", last)
	}
}

func TestAdminDraftRoundTrip(t *testing.T) {
	store := &stubStore{}
	authorizer := &fakeAuthorizer{principal: reference.Principal{
		UserID: uuid.New(), Roles: []string{"admin"}, Permissions: []string{reference.PermWrite}, RequestID: "req-1",
	}}
	handler := newTestServer(t, authorizer, store)
	document, err := seed.Document().CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{
		"expected_revision": 0,
		"document":          json.RawMessage(document),
	})
	request := httptest.NewRequest(http.MethodPut, "/v1/admin/reference-data/draft", strings.NewReader(string(body)))
	request.Header.Set("Authorization", "Bearer session-token")
	request.Header.Set("X-Step-Up-Token", "step-up-token")
	request.Header.Set("X-Request-ID", "req-1")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("put draft status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	if store.saved.ContentHash == "" || store.saved.Revision != 1 {
		t.Fatalf("draft was not saved correctly: %+v", store.saved)
	}
	if len(authorizer.calls) != 1 || authorizer.calls[0].token != "session-token" {
		t.Fatalf("unexpected authorization calls: %+v", authorizer.calls)
	}

	// A malformed document is rejected before it reaches persistence.
	request = httptest.NewRequest(http.MethodPut, "/v1/admin/reference-data/draft",
		strings.NewReader(`{"expected_revision":1,"document":{"schema_version":"wrong"}}`))
	request.Header.Set("Authorization", "Bearer session-token")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid document status = %d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestAdminRegistryListsAssetsAndNetworks(t *testing.T) {
	document := seed.Document()
	canonical, err := document.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	store := &stubStore{draft: reference.Draft{
		Revision: 4, ContentHash: refdata.ContentHash(canonical), Document: document, Canonical: canonical,
	}}
	authorizer := &fakeAuthorizer{principal: reference.Principal{
		UserID: uuid.New(), Permissions: []string{reference.PermRead}, RequestID: "req-list",
	}}
	handler := newTestServer(t, authorizer, store)
	for _, path := range []string{
		"/v1/admin/reference-data/assets",
		"/v1/admin/reference-data/networks",
	} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set("Authorization", "Bearer session-token")
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s status = %d body=%s", path, recorder.Code, recorder.Body.String())
		}
		var body map[string]any
		if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s response is not JSON: %v", path, err)
		}
		if body["content_hash"] != refdata.ContentHash(canonical) || body["revision"].(float64) != 4 {
			t.Fatalf("%s returned an unexpected registry view: %v", path, body)
		}
	}
	if len(authorizer.calls) != 2 || authorizer.calls[0].permission != reference.PermRead {
		t.Fatalf("unexpected authorization calls: %+v", authorizer.calls)
	}
}

func TestInternalSnapshotRequiresServiceTokenAndServesVerifiedBody(t *testing.T) {
	store := &stubStore{}
	authorizer := &fakeAuthorizer{principal: reference.Principal{UserID: uuid.New()}}
	handler := newTestServer(t, authorizer, store)

	request := httptest.NewRequest(http.MethodGet, "/v1/reference-data/active?shard_id=default", nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("missing service token status = %d, want 401", recorder.Code)
	}

	request = httptest.NewRequest(http.MethodGet, "/v1/reference-data/active?shard_id=default", nil)
	request.Header.Set("X-Nexora-Service-Token", "internal-test-token")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("snapshot status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("X-Nexora-Config-Version") != "3" {
		t.Fatalf("missing version header: %v", recorder.Header())
	}
	hash := recorder.Header().Get("X-Nexora-Config-Hash")
	if err := refdata.VerifyHash(recorder.Body.Bytes(), hash); err != nil {
		t.Fatalf("served snapshot hash mismatch: %v", err)
	}
	if recorder.Header().Get("X-Nexora-Config-Schema") != refdata.SchemaVersion {
		t.Fatalf("unexpected schema header: %v", recorder.Header())
	}
}
