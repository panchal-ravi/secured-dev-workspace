// Package llmgw is the portal's admin client for the shared LiteLLM gateway. It
// is a Go port of terraform/project/scripts/llm-provision.sh extended with the
// model-management calls the Platform Admin onboarding flow needs: add/list/delete
// models, mint a scoped virtual key (budget + rpm), run a test completion through
// it, and revoke it — the exact path a Project Admin uses to consume a model.
//
// Auth is the gateway admin key (Authorization: Bearer). The portal uses a
// dedicated non-master "portal-admin" key from Vault, never the master key.
package llmgw

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client is the LiteLLM admin surface the onboarding flows depend on. Implemented
// by *httpClient; an interface so the admin service can be tested with a fake.
type Client interface {
	// AddModel registers a model in the gateway DB (STORE_MODEL_IN_DB mode).
	AddModel(ctx context.Context, in AddModelInput) error
	// ListModels returns the models the gateway currently serves (config + DB).
	ListModels(ctx context.Context) ([]Model, error)
	// DeleteModel removes a DB-stored model by its LiteLLM model id.
	DeleteModel(ctx context.Context, modelID string) error
	// GenerateKey mints a virtual key scoped to models, with a budget and rpm
	// limit, and returns the key string.
	GenerateKey(ctx context.Context, spec KeySpec) (key string, err error)
	// DeleteKeyByAlias hard-deletes a virtual key by alias (frees the alias).
	DeleteKeyByAlias(ctx context.Context, alias string) error
	// TestCompletion sends a minimal completion through key for model and returns
	// the HTTP status (200 success, 429 rate-limited, 401 revoked/invalid). A
	// non-2xx status is NOT an error here — the caller asserts on the code.
	TestCompletion(ctx context.Context, key, model, prompt string) (status int, err error)
}

// Model is a model the gateway serves. ID is the LiteLLM-assigned id (needed to
// delete a DB model); Source distinguishes static config models from DB ones.
// Backend/Provider come from the model's litellm_params so the inventory can show
// what every model maps to, including config models with no Portal overlay.
type Model struct {
	Name     string `json:"name"`
	ID       string `json:"id"`
	Source   string `json:"source"`   // "config" (read-only) or "db"
	Backend  string `json:"backend"`  // litellm_params.model, e.g. "deepseek/deepseek-chat"
	Provider string `json:"provider"` // custom_llm_provider, else the prefix of Backend
}

// AddModelInput is a model registration. LiteLLMParams carries the provider
// mapping (e.g. {"model":"deepseek/deepseek-chat","api_key":"<from Vault>"}).
type AddModelInput struct {
	ModelName     string         `json:"model_name"`
	LiteLLMParams map[string]any `json:"litellm_params"`
}

// KeySpec is a virtual-key request. Budget is a USD soft cap; RPMLimit is
// requests/minute (<=0 omits the limit).
type KeySpec struct {
	Alias     string
	Models    []string
	MaxBudget float64
	RPMLimit  int
	Metadata  map[string]string
}

type httpClient struct {
	baseURL  string
	adminKey string
	hc       *http.Client
}

// New builds a LiteLLM admin client. baseURL is the gateway URL (e.g.
// http://<node>:4000); adminKey is the portal-admin bearer key from Vault.
func New(baseURL, adminKey string, hc *http.Client) Client {
	if hc == nil {
		hc = &http.Client{Timeout: 60 * time.Second}
	}
	return &httpClient{baseURL: strings.TrimRight(baseURL, "/"), adminKey: adminKey, hc: hc}
}

func (c *httpClient) AddModel(ctx context.Context, in AddModelInput) error {
	body, _ := json.Marshal(map[string]any{
		"model_name":     in.ModelName,
		"litellm_params": in.LiteLLMParams,
	})
	return c.call(ctx, http.MethodPost, "/model/new", body, nil)
}

func (c *httpClient) ListModels(ctx context.Context) ([]Model, error) {
	// /model/info exposes both the public name and the LiteLLM id (needed to
	// delete) plus db_model to tell DB models from static config ones.
	var resp struct {
		Data []struct {
			ModelName     string `json:"model_name"`
			LiteLLMParams struct {
				Model             string `json:"model"`
				CustomLLMProvider string `json:"custom_llm_provider"`
			} `json:"litellm_params"`
			ModelInfo struct {
				ID      string `json:"id"`
				DBModel bool   `json:"db_model"`
			} `json:"model_info"`
		} `json:"data"`
	}
	if err := c.call(ctx, http.MethodGet, "/model/info", nil, &resp); err != nil {
		return nil, err
	}
	out := make([]Model, 0, len(resp.Data))
	for _, m := range resp.Data {
		src := "config"
		if m.ModelInfo.DBModel {
			src = "db"
		}
		backend := m.LiteLLMParams.Model
		provider := m.LiteLLMParams.CustomLLMProvider
		if provider == "" {
			// litellm_params.model is "<provider>/<model>"; the prefix is the provider.
			if i := strings.Index(backend, "/"); i > 0 {
				provider = backend[:i]
			}
		}
		out = append(out, Model{Name: m.ModelName, ID: m.ModelInfo.ID, Source: src, Backend: backend, Provider: provider})
	}
	return out, nil
}

func (c *httpClient) DeleteModel(ctx context.Context, modelID string) error {
	body, _ := json.Marshal(map[string]string{"id": modelID})
	return c.call(ctx, http.MethodPost, "/model/delete", body, nil)
}

func (c *httpClient) GenerateKey(ctx context.Context, spec KeySpec) (string, error) {
	payload := map[string]any{
		"key_alias": spec.Alias,
		"models":    spec.Models,
	}
	if spec.MaxBudget > 0 {
		payload["max_budget"] = spec.MaxBudget
	}
	if spec.RPMLimit > 0 {
		payload["rpm_limit"] = spec.RPMLimit
	}
	if len(spec.Metadata) > 0 {
		payload["metadata"] = spec.Metadata
	}
	body, _ := json.Marshal(payload)
	var resp struct {
		Key string `json:"key"`
	}
	if err := c.call(ctx, http.MethodPost, "/key/generate", body, &resp); err != nil {
		return "", err
	}
	if resp.Key == "" {
		return "", fmt.Errorf("llmgw: gateway returned no key for alias %q", spec.Alias)
	}
	return resp.Key, nil
}

func (c *httpClient) DeleteKeyByAlias(ctx context.Context, alias string) error {
	body, _ := json.Marshal(map[string]any{"key_aliases": []string{alias}})
	return c.call(ctx, http.MethodPost, "/key/delete", body, nil)
}

func (c *httpClient) TestCompletion(ctx context.Context, key, model, prompt string) (int, error) {
	if prompt == "" {
		prompt = "ping"
	}
	body, _ := json.Marshal(map[string]any{
		"model":      model,
		"max_tokens": 8,
		"messages":   []map[string]string{{"role": "user", "content": prompt}},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return 0, fmt.Errorf("llmgw: build completion request: %w", err)
	}
	// Authenticate AS the virtual key (not the admin key) so the call exercises
	// the key's model scope, budget, and rpm limit — the consumption path.
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return 0, fmt.Errorf("llmgw: test completion: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, nil
}

// call performs an admin-authenticated request and decodes JSON into out (nil
// discards). A non-2xx status is an error.
func (c *httpClient) call(ctx context.Context, method, path string, body []byte, out any) error {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rdr)
	if err != nil {
		return fmt.Errorf("llmgw: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.adminKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("llmgw: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("llmgw: %s %s: status %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("llmgw: decode %s %s response: %w", method, path, err)
	}
	return nil
}
