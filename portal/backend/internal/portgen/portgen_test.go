package portgen

import "testing"

func TestAllocateLowestFree(t *testing.T) {
	got, err := Allocate(Range{Min: 2222, Max: 2399}, []int{2222, 2223, 2225})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 2224 {
		t.Fatalf("want 2224, got %d", got)
	}
}

func TestAllocateEmptyUsed(t *testing.T) {
	got, err := Allocate(Range{Min: 2222, Max: 2399}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 2222 {
		t.Fatalf("want 2222, got %d", got)
	}
}

func TestAllocateExhausted(t *testing.T) {
	r := Range{Min: 2222, Max: 2224}
	if _, err := Allocate(r, []int{2222, 2223, 2224}); err == nil {
		t.Fatal("expected exhaustion error, got nil")
	}
}

func TestAllocateInvalidRange(t *testing.T) {
	if _, err := Allocate(Range{Min: 2400, Max: 2222}, nil); err == nil {
		t.Fatal("expected invalid-range error, got nil")
	}
}

func TestAllocateIgnoresOutOfRangeUsed(t *testing.T) {
	// Ports in use outside the range must not affect allocation.
	got, err := Allocate(Range{Min: 2222, Max: 2399}, []int{22, 80, 443})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 2222 {
		t.Fatalf("want 2222, got %d", got)
	}
}
