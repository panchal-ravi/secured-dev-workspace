// Package portgen allocates a host SSH port for a new workspace.
//
// Workspace SSH ports are published on the shared node's private IP and are
// therefore NODE-GLOBAL: every workspace across every project must use a
// distinct port. The caller collects the ports already in use (from Boundary
// targets and Nomad jobs) and asks for the lowest free port in a fixed range.
package portgen

import "fmt"

// Range is an inclusive [Min, Max] span of candidate host ports.
type Range struct {
	Min int
	Max int
}

// DefaultRange is the PoC allocation window for workspace SSH ports.
var DefaultRange = Range{Min: 2222, Max: 2399}

// Allocate returns the lowest port in r that is not present in used. It returns
// an error if the range is exhausted or invalid.
func Allocate(r Range, used []int) (int, error) {
	if r.Min > r.Max || r.Min <= 0 {
		return 0, fmt.Errorf("portgen: invalid range %d-%d", r.Min, r.Max)
	}
	taken := make(map[int]struct{}, len(used))
	for _, p := range used {
		taken[p] = struct{}{}
	}
	for p := r.Min; p <= r.Max; p++ {
		if _, ok := taken[p]; !ok {
			return p, nil
		}
	}
	return 0, fmt.Errorf("portgen: no free port in range %d-%d (%d in use)", r.Min, r.Max, len(used))
}
