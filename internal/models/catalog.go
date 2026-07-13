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

// DefaultModels is the bundled fallback list.
var DefaultModels = []ModelEntry{
	{ID: "big-pickle", Object: "model", OwnedBy: "opencode"},
	{ID: "deepseek-v4-flash-free", Object: "model", OwnedBy: "opencode"},
	{ID: "hy3-free", Object: "model", OwnedBy: "opencode"},
	{ID: "mimo-v2.5-free", Object: "model", OwnedBy: "opencode"},
	{ID: "nemotron-3-ultra-free", Object: "model", OwnedBy: "opencode"},
	{ID: "north-mini-code-free", Object: "model", OwnedBy: "opencode"},
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
		models: DefaultModels,
	}
}

type upstreamModelsResponse struct {
	Data []json.RawMessage `json:"data"`
}

func (c *Catalog) Fetch() error {
	return c.FetchWithContext(context.Background())
}

// FetchWithContext fetches the model list from upstream with a context for cancellation.
func (c *Catalog) FetchWithContext(ctx context.Context) error {
	url := c.upstreamURL + "/models"
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
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

// FilteredModels returns models filtered by the default criteria.
// big-pickle is always included, plus any model ending in -free.
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
