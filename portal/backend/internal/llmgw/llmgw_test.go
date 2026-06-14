package llmgw

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeGateway is a minimal LiteLLM admin stand-in. Models live in a map; a virtual
// key is the literal "sk-<alias>" and is valid until deleted. rpm is simulated by
// failing the second completion for a key whose alias contains "rpm1".
type fakeGateway struct {
	adminKey string
	models   map[string]string // id -> model_name
	keys     map[string]string // alias -> key
	calls    map[string]int    // key -> completion count
}

func (f *fakeGateway) handler() http.Handler {
	mux := http.NewServeMux()
	requireAdmin := func(r *http.Request) bool {
		return r.Header.Get("Authorization") == "Bearer "+f.adminKey
	}

	mux.HandleFunc("POST /model/new", func(w http.ResponseWriter, r *http.Request) {
		if !requireAdmin(r) {
			w.WriteHeader(401)
			return
		}
		var in struct {
			ModelName string `json:"model_name"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.models["id-"+in.ModelName] = in.ModelName
		w.WriteHeader(200)
	})
	mux.HandleFunc("GET /model/info", func(w http.ResponseWriter, r *http.Request) {
		if !requireAdmin(r) {
			w.WriteHeader(401)
			return
		}
		var data []map[string]any
		for id, name := range f.models {
			data = append(data, map[string]any{
				"model_name": name,
				"model_info": map[string]any{"id": id, "db_model": true},
			})
		}
		writeJSON(w, 200, map[string]any{"data": data})
	})
	mux.HandleFunc("POST /model/delete", func(w http.ResponseWriter, r *http.Request) {
		if !requireAdmin(r) {
			w.WriteHeader(401)
			return
		}
		var in struct {
			ID string `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		delete(f.models, in.ID)
		w.WriteHeader(200)
	})
	mux.HandleFunc("POST /key/generate", func(w http.ResponseWriter, r *http.Request) {
		if !requireAdmin(r) {
			w.WriteHeader(401)
			return
		}
		var in struct {
			Alias string `json:"key_alias"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		key := "sk-" + in.Alias
		f.keys[in.Alias] = key
		writeJSON(w, 200, map[string]string{"key": key})
	})
	mux.HandleFunc("POST /key/delete", func(w http.ResponseWriter, r *http.Request) {
		if !requireAdmin(r) {
			w.WriteHeader(401)
			return
		}
		var in struct {
			Aliases []string `json:"key_aliases"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		for _, a := range in.Aliases {
			delete(f.keys, a)
		}
		w.WriteHeader(200)
	})
	mux.HandleFunc("POST /chat/completions", func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		alias := strings.TrimPrefix(key, "sk-")
		if f.keys[alias] != key { // unknown/revoked key
			w.WriteHeader(401)
			return
		}
		f.calls[key]++
		if strings.Contains(alias, "rpm1") && f.calls[key] > 1 {
			w.WriteHeader(429)
			return
		}
		writeJSON(w, 200, map[string]any{"choices": []map[string]any{{"message": map[string]string{"content": "ok"}}}})
	})
	return mux
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func newFake() *fakeGateway {
	return &fakeGateway{adminKey: "sk-admin", models: map[string]string{}, keys: map[string]string{}, calls: map[string]int{}}
}

func TestModelLifecycle(t *testing.T) {
	f := newFake()
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	c := New(srv.URL, "sk-admin", srv.Client())
	ctx := context.Background()

	if err := c.AddModel(ctx, AddModelInput{ModelName: "deepseek-v4-pro", LiteLLMParams: map[string]any{"model": "deepseek/deepseek-chat", "api_key": "x"}}); err != nil {
		t.Fatalf("AddModel: %v", err)
	}
	models, err := c.ListModels(ctx)
	if err != nil || len(models) != 1 || models[0].Name != "deepseek-v4-pro" || models[0].Source != "db" {
		t.Fatalf("ListModels: %+v err=%v", models, err)
	}
	if err := c.DeleteModel(ctx, models[0].ID); err != nil {
		t.Fatalf("DeleteModel: %v", err)
	}
	if models, _ := c.ListModels(ctx); len(models) != 0 {
		t.Fatalf("model not deleted: %+v", models)
	}
}

func TestConsumptionMirror(t *testing.T) {
	f := newFake()
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	c := New(srv.URL, "sk-admin", srv.Client())
	ctx := context.Background()

	// scoped key with rpm=1 (alias marks the fake to 429 on the 2nd call)
	key, err := c.GenerateKey(ctx, KeySpec{Alias: "llm-test-rpm1", Models: []string{"deepseek-v4-pro"}, MaxBudget: 1, RPMLimit: 1})
	if err != nil || key == "" {
		t.Fatalf("GenerateKey: key=%q err=%v", key, err)
	}

	// 1st completion: 200
	if st, err := c.TestCompletion(ctx, key, "deepseek-v4-pro", "ping"); err != nil || st != 200 {
		t.Fatalf("completion 1: status=%d err=%v", st, err)
	}
	// 2nd completion: 429 (rate limited)
	if st, err := c.TestCompletion(ctx, key, "deepseek-v4-pro", "ping"); err != nil || st != 429 {
		t.Fatalf("completion 2: status=%d err=%v want 429", st, err)
	}
	// revoke → 401
	if err := c.DeleteKeyByAlias(ctx, "llm-test-rpm1"); err != nil {
		t.Fatalf("DeleteKeyByAlias: %v", err)
	}
	if st, err := c.TestCompletion(ctx, key, "deepseek-v4-pro", "ping"); err != nil || st != 401 {
		t.Fatalf("completion after revoke: status=%d err=%v want 401", st, err)
	}
}
