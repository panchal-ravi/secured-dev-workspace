package blueprint

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"sort"
)

//go:embed seeds/*.json
var seedFS embed.FS

// SeedManifests loads the checked-in exemplar blueprints (one per class), sorted by
// id. They are the Class A/B/C proofs the integration validation exercises.
func SeedManifests() ([]BlueprintManifest, error) {
	entries, err := fs.ReadDir(seedFS, "seeds")
	if err != nil {
		return nil, fmt.Errorf("blueprint: read seeds dir: %w", err)
	}
	var out []BlueprintManifest
	for _, e := range entries {
		b, err := seedFS.ReadFile("seeds/" + e.Name())
		if err != nil {
			return nil, fmt.Errorf("blueprint: read seed %s: %w", e.Name(), err)
		}
		var m BlueprintManifest
		if err := json.Unmarshal(b, &m); err != nil {
			return nil, fmt.Errorf("blueprint: parse seed %s: %w", e.Name(), err)
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
