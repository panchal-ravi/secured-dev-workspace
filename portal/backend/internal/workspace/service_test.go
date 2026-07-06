package workspace

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/secured-dev-workspace/developer-portal/internal/apperr"
	"github.com/secured-dev-workspace/developer-portal/internal/descriptor"
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
