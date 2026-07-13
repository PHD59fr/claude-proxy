package config

import (
	"fmt"
	"net/url"
	"strings"
)

func (c *Config) Validate() []string {
	var issues []string
	if c.UpstreamBaseURL == "" {
		issues = append(issues, "upstream_base_url is required")
	}
	if len(c.Models) == 0 {
		issues = append(issues, "at least one model is required (set the ordered models list, 1st = default)")
	}
	if c.ListenAddr == "" {
		issues = append(issues, "listen_addr is required")
	}
	if !validHTTPURL(c.UpstreamBaseURL) {
		issues = append(issues, "upstream_base_url must be an absolute HTTP(S) URL")
	}
	if c.RequestTimeout <= 0 {
		issues = append(issues, "request_timeout must be positive")
	}
	if c.ModelCacheTTL <= 0 {
		issues = append(issues, "model_cache_ttl must be positive")
	}
	if c.MaxBodySize <= 0 {
		issues = append(issues, "max_body_size must be positive")
	}
	if c.LogLevel != "debug" && c.LogLevel != "info" && c.LogLevel != "warn" && c.LogLevel != "error" {
		issues = append(issues, "log_level must be debug, info, warn, or error")
	}
	if c.LogFormat != "text" && c.LogFormat != "json" {
		issues = append(issues, "log_format must be text or json")
	}
	if c.PassthroughAPIKey && c.UpstreamAPIKey != "" && c.UpstreamAPIKey != "public" {
		issues = append(issues, "UPSTREAM_API_KEY_PASSTHROUGH=true cannot be combined with a non-default UPSTREAM_API_KEY")
	}
	if c.PassthroughAPIKey {
		for _, u := range c.Upstreams {
			if u.APIKey != "" && u.APIKey != "public" {
				issues = append(issues, "UPSTREAM_API_KEY_PASSTHROUGH=true cannot be combined with an API key for upstream "+u.Name)
			}
		}
	}
	if (c.CodexOAuthToken == "") != (c.CodexAccountID == "") {
		issues = append(issues, "codex_oauth_token and codex_account_id must both be set or both be empty")
	}

	// Validate that every configured model references a known upstream.
	known := map[string]bool{
		DefaultUpstreamName: true,
		CodexUpstreamName:   true,
	}
	for _, u := range c.Upstreams {
		name := strings.TrimSpace(u.Name)
		if name == "" {
			issues = append(issues, "upstream entry with empty name")
			continue
		}
		if known[name] {
			issues = append(issues, "upstream name "+name+" is reserved or duplicated")
			continue
		}
		if !validHTTPURL(strings.TrimSpace(u.BaseURL)) {
			issues = append(issues, "upstream "+name+" must have an absolute HTTP(S) base_url")
		}
		known[name] = true
	}
	for _, m := range c.Models {
		if m.Name == "" {
			issues = append(issues, "model entry with empty name")
			continue
		}
		if !known[m.Upstream] {
			issues = append(issues, "model "+m.Name+" references unknown upstream "+m.Upstream)
		}
	}
	return issues
}

func validHTTPURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

func (c *Config) MaskedKey() string {
	if c.UpstreamAPIKey == "" {
		return ""
	}
	k := c.UpstreamAPIKey
	if len(k) <= 8 {
		return "***"
	}
	return k[:4] + "..." + k[len(k)-4:]
}

func (c *Config) String() string {
	var b strings.Builder
	b.WriteString("Config{\n")
	fmt.Fprintf(&b, "  ListenAddr: %s\n", c.ListenAddr)
	fmt.Fprintf(&b, "  UpstreamBaseURL: %s\n", c.UpstreamBaseURL)
	fmt.Fprintf(&b, "  UpstreamAPIKey: %s\n", c.MaskedKey())
	fmt.Fprintf(&b, "  InboundAPIKey: %s\n", boolStr(c.InboundAPIKey != "", "***", "(none)"))
	fmt.Fprintf(&b, "  PassthroughAPIKey: %v\n", c.PassthroughAPIKey)
	fmt.Fprintf(&b, "  DefaultModel: %s\n", c.DefaultModel)
	if len(c.Models) > 0 {
		parts := make([]string, len(c.Models))
		for i, m := range c.Models {
			parts[i] = m.Name + "@" + m.Upstream
		}
		fmt.Fprintf(&b, "  Models (ordered): %s\n", strings.Join(parts, ", "))
	}
	fmt.Fprintf(&b, "  ReasoningModel: %s\n", boolStr(c.ReasoningModel != "", c.ReasoningModel, "(default)"))
	fmt.Fprintf(&b, "  CompletionModel: %s\n", boolStr(c.CompletionModel != "", c.CompletionModel, "(default)"))
	fmt.Fprintf(&b, "  AllowUnlisted: %v\n", c.AllowUnlisted)
	fmt.Fprintf(&b, "  ExposeAllModels: %v\n", c.ExposeAllModels)

	fmt.Fprintf(&b, "  RequestTimeout: %s\n", c.RequestTimeout)
	fmt.Fprintf(&b, "  ModelCacheTTL: %s\n", c.ModelCacheTTL)
	fmt.Fprintf(&b, "  MaxBodySize: %d\n", c.MaxBodySize)
	fmt.Fprintf(&b, "  LogLevel: %s\n", c.LogLevel)
	fmt.Fprintf(&b, "  LogFormat: %s\n", c.LogFormat)
	b.WriteString("}")
	return b.String()
}

func boolStr(b bool, t, f string) string {
	if b {
		return t
	}
	return f
}
