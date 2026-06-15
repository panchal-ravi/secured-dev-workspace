package blueprint

import "testing"

func TestSeedManifests_AllValidAndOneOfEachClass(t *testing.T) {
	seeds, err := SeedManifests()
	if err != nil {
		t.Fatal(err)
	}
	if len(seeds) != 3 {
		t.Fatalf("expected 3 seeds, got %d", len(seeds))
	}
	classes := map[string]bool{}
	for _, m := range seeds {
		if err := m.Validate(); err != nil {
			t.Errorf("seed %s@%d invalid: %v", m.ID, m.Version, err)
		}
		classes[m.Class] = true
	}
	for _, c := range []string{ClassA, ClassB, ClassC} {
		if !classes[c] {
			t.Errorf("missing a class %s seed", c)
		}
	}
}

func TestSeedManifests_PoliciesLintClean(t *testing.T) {
	seeds, _ := SeedManifests()
	for _, m := range seeds {
		mount := "secret"
		role := ""
		if m.Class == ClassA {
			mount = render(m.Engines[0].MountPathTpl, "probe")
			role = render(m.Role.NameTpl, "probe")
		}
		p, err := RenderPolicy(m.PolicyTpl, PolicyVars{Namespace: "probe", Mount: mount, Role: role})
		if err != nil {
			t.Fatalf("%s render: %v", m.ID, err)
		}
		if err := LintPolicy(p, allowedPrefixes("probe", "secret", mount)); err != nil {
			t.Errorf("%s policy fails lint: %v", m.ID, err)
		}
	}
}
