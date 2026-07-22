package jobtemplate

import (
	"testing"

	"github.com/secured-dev-workspace/developer-portal/internal/store"
)

func TestResolveAgents_DefaultAndOverlay(t *testing.T) {
	// No settings → every agent enabled, registry order (claude first).
	all := ResolveAgents(nil)
	if len(all) != 2 || all[0].Key != "claude" || all[1].Key != "bob" {
		t.Fatalf("unexpected registry: %+v", all)
	}
	for _, a := range all {
		if !a.Enabled {
			t.Fatalf("agent %q should default enabled", a.Key)
		}
	}

	// A disable row overlays; an absent one stays enabled.
	got := ResolveAgents([]store.CodingAgentSetting{{Key: "bob", Enabled: false}})
	for _, a := range got {
		if a.Key == "bob" && a.Enabled {
			t.Fatalf("bob should be disabled")
		}
		if a.Key == "claude" && !a.Enabled {
			t.Fatalf("claude should remain enabled")
		}
	}
}

func TestIsAgentEnabled(t *testing.T) {
	if !IsAgentEnabled("claude", nil) {
		t.Fatalf("claude should be enabled by default")
	}
	if IsAgentEnabled("grok", nil) {
		t.Fatalf("unknown agent must not be enabled")
	}
	if IsAgentEnabled("bob", []store.CodingAgentSetting{{Key: "bob", Enabled: false}}) {
		t.Fatalf("disabled bob must not be enabled")
	}
}

func TestAgentFeature(t *testing.T) {
	f, ok := AgentFeature("bob")
	if !ok || f.Key != "bob-shell-ibm-hosted" {
		t.Fatalf("bob feature wrong: %+v ok=%v", f, ok)
	}
	if _, ok := AgentFeature("grok"); ok {
		t.Fatalf("unknown agent should have no feature")
	}
}
