package workspace

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/secured-dev-workspace/developer-portal/internal/apperr"
	"github.com/secured-dev-workspace/developer-portal/internal/descriptor"
	"github.com/secured-dev-workspace/developer-portal/internal/portgen"
)

// Create must refuse to launch a workspace while the project's Vault engines
// are not fully configured: the rendered job template blocks forever on
// vault.read (e.g. github/token/dev-workspace) and the workspace hangs in
// "pending" with no visible error. The guard fires before any Nomad/Boundary
// call, so nil clients prove it runs first.
func TestCreate_RejectsUnprovisionedProject(t *testing.T) {
	s := New(Config{}, nil, nil, nil)

	cases := []struct {
		name string
		d    descriptor.Descriptor
		want string
	}{
		{
			name: "engines not provisioned",
			d:    descriptor.Descriptor{ProjectName: "project-acme"},
			want: "not fully provisioned",
		},
		{
			name: "github access not configured",
			d:    descriptor.Descriptor{ProjectName: "project-acme", CredentialLibraryID: "clvlt_abc"},
			want: "GitHub access",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.Create(context.Background(), tc.d, CreateInput{Handle: "alice"})
			if err == nil {
				t.Fatal("Create succeeded on an unprovisioned project")
			}
			if !errors.Is(err, apperr.ErrConflict) {
				t.Fatalf("want apperr.ErrConflict, got %v", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

// reserve is the race-critical section of port allocation: concurrent Create
// calls read the same stale UsedPorts snapshot (here, the empty set) before any
// has published its port, so the in-flight reserved set is the only thing that
// keeps them from picking the same port. N goroutines reserving against the same
// snapshot must all get distinct, in-range ports.
func TestReserve_ConcurrentDistinctPorts(t *testing.T) {
	s := New(Config{PortRange: portgen.Range{Min: 2222, Max: 2399}}, nil, nil, nil)

	const n = 50
	ports := make([]int, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Hold the reservation (no release) to mirror in-flight creates.
			p, _, err := s.reserve(nil)
			ports[i], errs[i] = p, err
		}(i)
	}
	wg.Wait()

	seen := map[int]bool{}
	for i, p := range ports {
		if errs[i] != nil {
			t.Fatalf("reserve %d: %v", i, errs[i])
		}
		if p < 2222 || p > 2399 {
			t.Fatalf("port %d out of range [2222,2399]", p)
		}
		if seen[p] {
			t.Fatalf("duplicate port %d handed out to concurrent callers", p)
		}
		seen[p] = true
	}
}

// A released port returns to the free pool for the next reservation.
func TestReserve_ReleaseFreesPort(t *testing.T) {
	s := New(Config{PortRange: portgen.Range{Min: 2222, Max: 2223}}, nil, nil, nil)

	p1, release1, err := s.reserve(nil)
	if err != nil {
		t.Fatalf("first reserve: %v", err)
	}
	p2, _, err := s.reserve(nil)
	if err != nil {
		t.Fatalf("second reserve: %v", err)
	}
	if p1 == p2 {
		t.Fatalf("two live reservations collided on %d", p1)
	}
	// Range is now exhausted; releasing p1 must free it for reuse.
	if _, _, err := s.reserve(nil); err == nil {
		t.Fatal("expected exhaustion error with both ports reserved")
	}
	release1()
	p3, _, err := s.reserve(nil)
	if err != nil {
		t.Fatalf("reserve after release: %v", err)
	}
	if p3 != p1 {
		t.Fatalf("released port %d not reused (got %d)", p1, p3)
	}
}

// TestIsVolumeInUse pins the retry gate for the workspace-Destroy home-volume
// delete: only the transient EBS detach race (EC2 VolumeInUse) is retried; any
// other CSI/Nomad error must surface immediately, never masked by the backoff.
func TestIsVolumeInUse(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		// The exact wrapped error Nomad surfaces during the detach race.
		{"ec2 volume-in-use", errors.New(`nomad: delete CSI volume "home-alice-x": Unexpected response code: 500 (controller delete volume: rpc error: VolumeInUse: Volume vol-049a is currently attached to i-043b)`), true},
		{"attached phrasing only", errors.New("Volume vol-1 is currently attached to i-2"), true},
		{"volumeinuse token only", errors.New("api error VolumeInUse"), true},
		{"unrelated 500", errors.New("controller plugin returned an internal error: connection refused"), false},
		{"not found", errors.New("volume not found"), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isVolumeInUse(c.err); got != c.want {
				t.Fatalf("isVolumeInUse(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}
