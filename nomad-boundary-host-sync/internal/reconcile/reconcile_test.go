package reconcile

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/secured-dev-workspace/nomad-boundary-host-sync/internal/nomad"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeNomad returns a preset service list.
type fakeNomad struct {
	svcs []nomad.WorkspaceService
	err  error
}

func (f *fakeNomad) ListWorkspaceServices(context.Context) ([]nomad.WorkspaceService, error) {
	return f.svcs, f.err
}

// fakeBoundary simulates Boundary state and records mutating calls.
type fakeBoundary struct {
	scopes   map[string]string   // project name -> scope id
	catalogs map[string]string   // scope id -> catalog id
	sets     map[string][]string // "catalogID/name" -> host ids ("" key means set absent)
	hostAddr map[string]string   // host id -> address
	hostErr  map[string]error    // host id -> forced HostAddress error

	created []string // "name@address"
	updated []string // "hostID@address"
}

func (f *fakeBoundary) ScopeIDByName(_ context.Context, name string) (string, bool, error) {
	id, ok := f.scopes[name]
	return id, ok, nil
}
func (f *fakeBoundary) CatalogIDByName(_ context.Context, scopeID string) (string, bool, error) {
	id, ok := f.catalogs[scopeID]
	return id, ok, nil
}
func (f *fakeBoundary) HostSetByName(_ context.Context, catalogID, name string) (string, []string, bool, error) {
	ids, ok := f.sets[catalogID+"/"+name]
	if !ok {
		return "", nil, false, nil
	}
	return "hsst_" + name, ids, true, nil
}
func (f *fakeBoundary) HostAddress(_ context.Context, hostID string) (string, error) {
	if err := f.hostErr[hostID]; err != nil {
		return "", err
	}
	return f.hostAddr[hostID], nil
}
func (f *fakeBoundary) CreateHostInSet(_ context.Context, _, _, name, address string) error {
	f.created = append(f.created, name+"@"+address)
	return nil
}
func (f *fakeBoundary) UpdateHostAddress(_ context.Context, hostID, address string) error {
	f.updated = append(f.updated, hostID+"@"+address)
	return nil
}

// baseBoundary is a fully-wired project "acme" with catalog "hcst_acme".
func baseBoundary() *fakeBoundary {
	return &fakeBoundary{
		scopes:   map[string]string{"acme": "p_acme"},
		catalogs: map[string]string{"p_acme": "hcst_acme"},
		sets:     map[string][]string{},
		hostAddr: map[string]string{},
		hostErr:  map[string]error{},
	}
}

func svc(name, project, addr string) nomad.WorkspaceService {
	return nomad.WorkspaceService{Name: name, Project: project, Address: addr, Port: 2222}
}

func TestEnsure_CreatesHostWhenSetEmpty(t *testing.T) {
	fb := baseBoundary()
	fb.sets["hcst_acme/ws-alice-abcde"] = []string{} // set exists, no host yet

	r := New(&fakeNomad{}, fb, testLogger())
	if err := r.ensure(context.Background(), svc("ws-alice-abcde", "acme", "10.0.1.5")); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if len(fb.created) != 1 || fb.created[0] != "ws-alice-abcde@10.0.1.5" {
		t.Fatalf("expected one create ws-alice-abcde@10.0.1.5, got %v", fb.created)
	}
	if len(fb.updated) != 0 {
		t.Fatalf("expected no updates, got %v", fb.updated)
	}
}

func TestEnsure_UpdatesWhenAddressChanged(t *testing.T) {
	fb := baseBoundary()
	fb.sets["hcst_acme/ws-alice-abcde"] = []string{"hst_1"}
	fb.hostAddr["hst_1"] = "10.0.1.5" // old node

	r := New(&fakeNomad{}, fb, testLogger())
	if err := r.ensure(context.Background(), svc("ws-alice-abcde", "acme", "10.0.2.9")); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if len(fb.updated) != 1 || fb.updated[0] != "hst_1@10.0.2.9" {
		t.Fatalf("expected update hst_1@10.0.2.9, got %v", fb.updated)
	}
	if len(fb.created) != 0 {
		t.Fatalf("expected no creates, got %v", fb.created)
	}
}

func TestEnsure_NoopWhenAddressSame(t *testing.T) {
	fb := baseBoundary()
	fb.sets["hcst_acme/ws-alice-abcde"] = []string{"hst_1"}
	fb.hostAddr["hst_1"] = "10.0.1.5"

	r := New(&fakeNomad{}, fb, testLogger())
	if err := r.ensure(context.Background(), svc("ws-alice-abcde", "acme", "10.0.1.5")); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if len(fb.created)+len(fb.updated) != 0 {
		t.Fatalf("expected no mutations, got created=%v updated=%v", fb.created, fb.updated)
	}
}

func TestEnsure_SkipsWhenSetMissing(t *testing.T) {
	fb := baseBoundary() // no set entry for the workspace

	r := New(&fakeNomad{}, fb, testLogger())
	if err := r.ensure(context.Background(), svc("ws-alice-abcde", "acme", "10.0.1.5")); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if len(fb.created)+len(fb.updated) != 0 {
		t.Fatalf("expected no mutations when host-set missing, got created=%v updated=%v", fb.created, fb.updated)
	}
}

func TestEnsure_SkipsWhenScopeMissing(t *testing.T) {
	fb := baseBoundary()

	r := New(&fakeNomad{}, fb, testLogger())
	if err := r.ensure(context.Background(), svc("ws-bob-xyzab", "unknown-project", "10.0.1.5")); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if len(fb.created)+len(fb.updated) != 0 {
		t.Fatalf("expected no mutations for unknown project, got created=%v updated=%v", fb.created, fb.updated)
	}
}

func TestReconcileOnce_PerServiceBestEffort(t *testing.T) {
	fb := baseBoundary()
	// Service A: healthy, needs a create.
	fb.sets["hcst_acme/ws-a-11111"] = []string{}
	// Service B: has a host whose address read fails — must not stop A.
	fb.sets["hcst_acme/ws-b-22222"] = []string{"hst_b"}
	fb.hostErr["hst_b"] = errors.New("boom")

	fn := &fakeNomad{svcs: []nomad.WorkspaceService{
		svc("ws-b-22222", "acme", "10.0.9.9"), // errors first
		svc("ws-a-11111", "acme", "10.0.1.1"),
	}}
	r := New(fn, fb, testLogger())
	r.ReconcileOnce(context.Background())

	if len(fb.created) != 1 || fb.created[0] != "ws-a-11111@10.0.1.1" {
		t.Fatalf("service A should still be created despite B's error, got %v", fb.created)
	}
}
