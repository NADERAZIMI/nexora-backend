package refdata

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	headerConfigVersion     = "X-Nexora-Config-Version"
	headerConfigHash        = "X-Nexora-Config-Hash"
	headerConfigSchema      = "X-Nexora-Config-Schema"
	headerConfigEffectiveAt = "X-Nexora-Config-Effective-At"
	headerConfigShard       = "X-Nexora-Config-Shard"
	headerConfigRollback    = "X-Nexora-Config-Rollback"
	headerServiceToken      = "X-Nexora-Service-Token" //nolint:gosec // header name, not a credential
	defaultMaxSnapshotBytes = 16 << 20
)

// HTTPClient fetches snapshots from the reference-data service.
type HTTPClient struct {
	BaseURL      string
	ServiceToken string
	HTTP         *http.Client
	MaxBodyBytes int64
}

// NewHTTPClient constructs a client with safe defaults.
func NewHTTPClient(baseURL, serviceToken string) *HTTPClient {
	return &HTTPClient{
		BaseURL:      strings.TrimRight(baseURL, "/"),
		ServiceToken: serviceToken,
		HTTP: &http.Client{
			Timeout: 10 * time.Second,
		},
		MaxBodyBytes: defaultMaxSnapshotBytes,
	}
}

// FetchActive loads the effective snapshot for a shard.
func (c *HTTPClient) FetchActive(ctx context.Context, shardID string) (Snapshot, error) {
	if strings.TrimSpace(shardID) == "" {
		shardID = "default"
	}
	return c.fetch(ctx, "/v1/reference-data/active?shard_id="+url.QueryEscape(shardID))
}

// FetchVersion loads a specific published version for recovery or rollback.
func (c *HTTPClient) FetchVersion(ctx context.Context, shardID string, version int64) (Snapshot, error) {
	if version <= 0 {
		return Snapshot{}, fmt.Errorf("version must be positive")
	}
	if strings.TrimSpace(shardID) == "" {
		shardID = "default"
	}
	return c.fetch(ctx, fmt.Sprintf("/v1/reference-data/versions/%d/snapshot?shard_id=%s", version, url.QueryEscape(shardID)))
}

func (c *HTTPClient) fetch(ctx context.Context, path string) (Snapshot, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return Snapshot{}, err
	}
	if c.ServiceToken != "" {
		request.Header.Set(headerServiceToken, c.ServiceToken)
	}
	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(request)
	if err != nil {
		return Snapshot{}, fmt.Errorf("fetch reference-data snapshot: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	limit := c.MaxBodyBytes
	if limit <= 0 {
		limit = defaultMaxSnapshotBytes
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return Snapshot{}, fmt.Errorf("read reference-data snapshot: %w", err)
	}
	if int64(len(body)) > limit {
		return Snapshot{}, fmt.Errorf("reference-data snapshot exceeds %d bytes", limit)
	}
	if response.StatusCode != http.StatusOK {
		return Snapshot{}, decodeAPIError(response.StatusCode, body)
	}

	version, err := strconv.ParseInt(response.Header.Get(headerConfigVersion), 10, 64)
	if err != nil || version <= 0 {
		return Snapshot{}, fmt.Errorf("reference-data response has an invalid version header")
	}
	hash := response.Header.Get(headerConfigHash)
	if err := VerifyHash(body, hash); err != nil {
		return Snapshot{}, err
	}
	document, err := Parse(body)
	if err != nil {
		return Snapshot{}, err
	}
	effectiveAt, err := time.Parse(time.RFC3339Nano, response.Header.Get(headerConfigEffectiveAt))
	if err != nil {
		return Snapshot{}, fmt.Errorf("reference-data response has an invalid effective timestamp")
	}
	schema := response.Header.Get(headerConfigSchema)
	if schema == "" {
		schema = document.SchemaVersion
	}
	if schema != document.SchemaVersion {
		return Snapshot{}, ErrSchema
	}
	return Snapshot{
		Version:       version,
		ContentHash:   strings.ToLower(hash),
		SchemaVersion: schema,
		EffectiveAt:   effectiveAt.UTC(),
		ShardID:       response.Header.Get(headerConfigShard),
		Rollback:      strings.EqualFold(response.Header.Get(headerConfigRollback), "true"),
		Document:      document,
		Canonical:     body,
	}, nil
}

// APIError is a stable reference-data error response.
type APIError struct {
	Status  int    `json:"-"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *APIError) Error() string {
	return fmt.Sprintf("reference-data API %s: %s", e.Code, e.Message)
}

func decodeAPIError(status int, body []byte) error {
	var envelope struct {
		Error APIError `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || envelope.Error.Code == "" {
		return &APIError{Status: status, Code: "REF_INTERNAL", Message: "reference-data request failed"}
	}
	envelope.Error.Status = status
	if envelope.Error.Message == "" {
		envelope.Error.Message = "reference-data request failed"
	}
	return &envelope.Error
}
