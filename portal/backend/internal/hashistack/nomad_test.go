package hashistack

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestResolvePlacement_DynamicPortFromFullAllocation pins the regression behind
// "no http host port assigned": the job-allocations LIST endpoint returns stubs
// WITHOUT AllocatedResources (Nomad only honors resources=true on /v1/allocations),
// so the host port MUST be read from the full allocation, not the stub.
func TestResolvePlacement_DynamicPortFromFullAllocation(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/job/mcp-p-x/allocations", func(w http.ResponseWriter, r *http.Request) {
		// List stub: placed (NodeID set) but no AllocatedResources — as served live.
		json.NewEncoder(w).Encode([]map[string]any{{
			"ID":            "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
			"NodeID":        "node-1",
			"DesiredStatus": "run",
		}})
	})
	mux.HandleFunc("/v1/allocation/aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"ID":     "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
			"NodeID": "node-1",
			"AllocatedResources": map[string]any{
				"Shared": map[string]any{
					"Ports": []map[string]any{
						{"Label": "http", "Value": 25872, "To": 8081, "HostIP": "10.0.0.5"},
					},
				},
			},
		})
	})
	mux.HandleFunc("/v1/node/node-1", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"ID":         "node-1",
			"HTTPAddr":   "10.0.0.5:4646",
			"Attributes": map[string]string{"unique.platform.aws.local-ipv4": "10.0.0.5"},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	n, err := NewNomad(srv.URL, "", TLSOptions{})
	if err != nil {
		t.Fatalf("NewNomad: %v", err)
	}
	ip, port, err := n.ResolvePlacement("project-p", "mcp-p-x")
	if err != nil {
		t.Fatalf("ResolvePlacement: %v", err)
	}
	if ip != "10.0.0.5" {
		t.Errorf("ip = %q, want 10.0.0.5", ip)
	}
	if port != 25872 {
		t.Errorf("port = %d, want 25872 (must come from the full allocation, not the stub)", port)
	}
}

// TestResolvePlacement_PicksNewestRunAllocOnUpdate pins the resubmit race: while a
// job update is rolling, the allocations list still contains the stopping alloc,
// listed FIRST (the API orders by ID, not recency). Taking the list head resolved
// the OLD alloc's host port, so the gateway peer was healed to a dying address
// (observed live: ContextForge deactivated the peer after 3 failed health checks).
// The placement must be the newest DesiredStatus=run allocation.
func TestResolvePlacement_PicksNewestRunAllocOnUpdate(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/job/mcp-p-x/allocations", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]any{
			{ // old alloc: still listed, marked for stop
				"ID": "0ddddddd-old0-old0-old0-eeeeeeeeeeee", "NodeID": "node-1",
				"DesiredStatus": "stop", "CreateIndex": 100,
			},
			{ // replacement alloc: run-desired, newer
				"ID": "cafecafe-new0-new0-new0-eeeeeeeeeeee", "NodeID": "node-1",
				"DesiredStatus": "run", "CreateIndex": 200,
			},
		})
	})
	mux.HandleFunc("/v1/allocation/0ddddddd-old0-old0-old0-eeeeeeeeeeee", func(w http.ResponseWriter, r *http.Request) {
		t.Error("resolved the stopping allocation — must pick the newest run-desired one")
		http.NotFound(w, r)
	})
	mux.HandleFunc("/v1/allocation/cafecafe-new0-new0-new0-eeeeeeeeeeee", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"ID":     "cafecafe-new0-new0-new0-eeeeeeeeeeee",
			"NodeID": "node-1",
			"AllocatedResources": map[string]any{
				"Shared": map[string]any{
					"Ports": []map[string]any{
						{"Label": "http", "Value": 21545, "To": 8082, "HostIP": "10.0.0.5"},
					},
				},
			},
		})
	})
	mux.HandleFunc("/v1/node/node-1", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"ID":         "node-1",
			"HTTPAddr":   "10.0.0.5:4646",
			"Attributes": map[string]string{"unique.platform.aws.local-ipv4": "10.0.0.5"},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	n, err := NewNomad(srv.URL, "", TLSOptions{})
	if err != nil {
		t.Fatalf("NewNomad: %v", err)
	}
	ip, port, err := n.ResolvePlacement("project-p", "mcp-p-x")
	if err != nil {
		t.Fatalf("ResolvePlacement: %v", err)
	}
	if ip != "10.0.0.5" {
		t.Errorf("ip = %q, want 10.0.0.5", ip)
	}
	if port != 21545 {
		t.Errorf("port = %d, want 21545 (the replacement alloc's port)", port)
	}
}
