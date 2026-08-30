package models

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

type ModelEntry struct {
	ID      string                 `json:"id"`
	Object  string                 `json:"object"`
	OwnedBy string                 `json:"owned_by"`
	Name    string                 `json:"name,omitempty"`
	Extra   map[string]interface{} `json:"-"`
}

// Catalog manages the upstream model list with caching.
type Catalog struct {
	mu          sync.RWMutex
	models      []ModelEntry
	lastFetch   time.Time
	cacheTTL    time.Duration
	upstreamURL string
	apiKey      string
	client      *http.Client
}

func NewCatalog(upstreamURL, apiKey string, cacheTTL time.Duration) *Catalog {
	return &Catalog{
		upstreamURL: strings.TrimRight(upstreamURL, "/"),
		apiKey:      apiKey,
		cacheTTL:    cacheTTL,
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// CachedModels returns the last successfully fetched catalog without causing
// network I/O.
func (c *Catalog) CachedModels() []ModelEntry {
	c.mu.RLock()
	defer c.mu.RUnlock()
	result := make([]ModelEntry, len(c.models))
	copy(result, c.models)
	return result
}

type upstreamModelsResponse struct {
	Data []json.RawMessage `json:"data"`
}

func (c *Catalog) Fetch() error {
	return c.FetchWithContext(context.Background())
}

// FetchWithContext fetches the model list from upstream with a context for cancellation.
func (c *Catalog) FetchWithContext(ctx context.Context) error {
	c.mu.RLock()
	upstreamURL, apiKey := c.upstreamURL, c.apiKey
	c.mu.RUnlock()
	url := upstreamURL + "/models"
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("fetch models: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fetch models: status %d", resp.StatusCode)
	}
	var result upstreamModelsResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result); err != nil {
		return fmt.Errorf("decode models: %w", err)
	}
	var models []ModelEntry
	for _, raw := range result.Data {
		var m ModelEntry
		if err := json.Unmarshal(raw, &m); err != nil {
			continue
		}
		if m.ID == "" {
			continue
		}
		models = append(models, m)
	}
	// Don't destroy the cache if upstream returns empty
	if len(models) > 0 {
		c.mu.Lock()
		c.models = models
		c.lastFetch = time.Now()
		c.mu.Unlock()
	}
	return nil
}

// Reconfigure updates the remote catalog settings and invalidates the cache.
// The cached model list is cleared too, so a switch to a different upstream does
// not keep serving the previous provider's stale catalog if the new one is
// temporarily unreachable (Fetch only overwrites models when non-empty).
func (c *Catalog) Reconfigure(upstreamURL, apiKey string, cacheTTL time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.upstreamURL = strings.TrimRight(upstreamURL, "/")
	c.apiKey = apiKey
	c.cacheTTL = cacheTTL
	c.lastFetch = time.Time{}
	c.models = nil
}

func (c *Catalog) GetModels(forceRefresh bool) []ModelEntry {
	c.mu.RLock()
	if !forceRefresh && time.Since(c.lastFetch) < c.cacheTTL {
		models := make([]ModelEntry, len(c.models))
		copy(models, c.models)
		c.mu.RUnlock()
		return models
	}
	c.mu.RUnlock()
	// Refresh
	_ = c.Fetch()
	c.mu.RLock()
	models := make([]ModelEntry, len(c.models))
	copy(models, c.models)
	c.mu.RUnlock()
	return models
}

// FilteredModels derives the current free Zen set from the remote catalog.
// big-pickle is the one free model whose ID does not carry the -free suffix.
func FilteredModels(models []ModelEntry) []ModelEntry {
	var filtered []ModelEntry
	seen := make(map[string]bool)
	for _, m := range models {
		if seen[m.ID] {
			continue
		}
		if m.ID == "big-pickle" || strings.HasSuffix(m.ID, "-free") {
			filtered = append(filtered, m)
			seen[m.ID] = true
		}
	}
	return filtered
}

// ToResponse formats models for the Anthropic-compatible /v1/models endpoint.
func ToResponse(models []ModelEntry, baseURL string) map[string]interface{} {
	data := make([]interface{}, 0, len(models))
	for _, m := range models {
		entry := map[string]interface{}{
			"type":     "model",
			"id":       m.ID,
			"owned_by": m.OwnedBy,
		}
		data = append(data, entry)
	}
	return map[string]interface{}{
		"object": "list",
		"data":   data,
	}
}
