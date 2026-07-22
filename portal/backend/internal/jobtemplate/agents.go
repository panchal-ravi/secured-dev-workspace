package jobtemplate

import "github.com/secured-dev-workspace/developer-portal/internal/store"

// Coding agents are an ORTHOGONAL dimension to the infra base template: the base
// (standard / GPU / microVM) carries no agent, and the project-admin picks an agent
// at flavor-create time. The set is code-known — every agent's binary is baked into
// the workspace image, so selecting one only changes how it is *wired* (which
// template blocks + entrypoint lines Inject emits), never what is installed. A
// platform-admin governs which agents are offered via the allow-list (an absent
// store row = enabled).

// DefaultAgent is used when a flavor-create request omits the agent (back-compat
// with callers written before the agent became selectable).
const DefaultAgent = "claude"

// Agent is a coding agent the platform can offer in a workspace.
type Agent struct {
	Key        string        `json:"key"`
	Label      string        `json:"label"`
	Governance string        `json:"governance,omitempty"` // caveat surfaced in the flavor picker
	Enabled    bool          `json:"enabled"`
	Feature    store.Feature `json:"-"` // flavor feature card added when this agent is chosen
}

// claudeFeature / bobFeature are the coding-agent cards, added to a flavor based on
// the selected agent (NOT baked into the infra base template's features anymore).
var claudeFeature = store.Feature{
	Key:         "claude-deepseek",
	Label:       "Claude Code CLI (governed model)",
	Description: "Pre-configured AI coding assistant. The API key is injected per session from Vault and never lands on the persistent home volume.",
}

// bobFeature states the governance caveat plainly: unlike Claude Code, Bob's LLM
// runs on IBM's hosted backend, NOT the governed LiteLLM gateway — so no per-project
// key, budget, or guardrail applies to its model traffic. MCP, git, and shared
// volumes still work as usual. You sign in interactively with your own IBMid the
// first time you run `bob` in the workspace (per-user identity; no shared key).
var bobFeature = store.Feature{
	Key:   "bob-shell-ibm-hosted",
	Label: "IBM Bob Shell CLI (IBM-hosted model)",
	Description: "Pre-configured IBM Bob Shell coding agent. Sign in with your IBMid the first " +
		"time you run `bob`. NOTE: Bob's LLM runs on IBM's hosted backend, NOT the governed " +
		"LiteLLM gateway — its model traffic is outside per-project keys, budgets, and guardrails.",
}

// agentDefs is the canonical, ordered registry of coding agents.
var agentDefs = []Agent{
	{
		Key:        "claude",
		Label:      "Claude Code",
		Governance: "Governed model access: per-project LiteLLM virtual key, budget, and guardrails apply.",
		Feature:    claudeFeature,
	},
	{
		Key:        "bob",
		Label:      "IBM Bob Shell",
		Governance: "Bob's LLM runs on IBM's hosted backend, NOT the governed LiteLLM gateway — its model traffic is outside per-project keys, budgets, and guardrails.",
		Feature:    bobFeature,
	},
}

// KnownAgent reports whether key is a registered coding agent.
func KnownAgent(key string) bool {
	for _, a := range agentDefs {
		if a.Key == key {
			return true
		}
	}
	return false
}

// AgentFeature returns the flavor feature card for a coding agent.
func AgentFeature(key string) (store.Feature, bool) {
	for _, a := range agentDefs {
		if a.Key == key {
			return a.Feature, true
		}
	}
	return store.Feature{}, false
}

// ResolveAgents overlays the persisted allow-list onto the registry, in registry
// order. An absent setting row means enabled, so a fresh install offers every agent.
func ResolveAgents(settings []store.CodingAgentSetting) []Agent {
	disabled := map[string]bool{}
	for _, s := range settings {
		if !s.Enabled {
			disabled[s.Key] = true
		}
	}
	out := make([]Agent, 0, len(agentDefs))
	for _, a := range agentDefs {
		a.Enabled = !disabled[a.Key]
		out = append(out, a)
	}
	return out
}

// IsAgentEnabled reports whether key is a known agent that the allow-list has not
// disabled (absent row = enabled).
func IsAgentEnabled(key string, settings []store.CodingAgentSetting) bool {
	if !KnownAgent(key) {
		return false
	}
	for _, s := range settings {
		if s.Key == key {
			return s.Enabled
		}
	}
	return true
}
