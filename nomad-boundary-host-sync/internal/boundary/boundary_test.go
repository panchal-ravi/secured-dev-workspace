package boundary

import (
	"errors"
	"fmt"
	"testing"

	bapi "github.com/hashicorp/boundary/api"
)

func TestIsAuthExpired(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"plain error", errors.New("boom"), false},
		{"401 unauthorized", bapi.ErrUnauthorized, true},
		{"wrapped 401", fmt.Errorf("boundary: list scopes: %w", bapi.ErrUnauthorized), true},
		{"403 forbidden", bapi.ErrPermissionDenied, false},
		{"404 not found", bapi.ErrNotFound, false},
	}
	for _, tt := range tests {
		if got := isAuthExpired(tt.err); got != tt.want {
			t.Errorf("%s: isAuthExpired = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestRetryOnAuthExpiry(t *testing.T) {
	t.Run("success first try — no reauth", func(t *testing.T) {
		calls, reauths := 0, 0
		err := retryOnAuthExpiry(
			func() error { calls++; return nil },
			func() error { reauths++; return nil },
		)
		if err != nil || calls != 1 || reauths != 0 {
			t.Fatalf("err=%v calls=%d reauths=%d", err, calls, reauths)
		}
	})

	t.Run("401 then success — reauth once, retried", func(t *testing.T) {
		calls, reauths := 0, 0
		err := retryOnAuthExpiry(
			func() error {
				calls++
				if calls == 1 {
					return bapi.ErrUnauthorized
				}
				return nil
			},
			func() error { reauths++; return nil },
		)
		if err != nil || calls != 2 || reauths != 1 {
			t.Fatalf("err=%v calls=%d reauths=%d", err, calls, reauths)
		}
	})

	t.Run("persistent 401 — returns error after one retry", func(t *testing.T) {
		calls, reauths := 0, 0
		err := retryOnAuthExpiry(
			func() error { calls++; return bapi.ErrUnauthorized },
			func() error { reauths++; return nil },
		)
		if !isAuthExpired(err) || calls != 2 || reauths != 1 {
			t.Fatalf("err=%v calls=%d reauths=%d", err, calls, reauths)
		}
	})

	t.Run("non-auth error — no reauth", func(t *testing.T) {
		calls, reauths := 0, 0
		want := errors.New("boom")
		err := retryOnAuthExpiry(
			func() error { calls++; return want },
			func() error { reauths++; return nil },
		)
		if !errors.Is(err, want) || calls != 1 || reauths != 0 {
			t.Fatalf("err=%v calls=%d reauths=%d", err, calls, reauths)
		}
	})

	t.Run("reauth fails — surfaces reauth error", func(t *testing.T) {
		reauthErr := errors.New("cannot authenticate")
		err := retryOnAuthExpiry(
			func() error { return bapi.ErrUnauthorized },
			func() error { return reauthErr },
		)
		if !errors.Is(err, reauthErr) {
			t.Fatalf("expected reauth error, got %v", err)
		}
	})
}
