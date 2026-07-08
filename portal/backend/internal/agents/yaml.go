package agents

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/secured-dev-workspace/developer-portal/internal/apperr"
	"gopkg.in/yaml.v3"
)

// nameRE matches the agent name: the same shape MCP servers use, so the derived
// Nomad job/service names are always valid.
var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,38}[a-z0-9]$`)

const (
	maxDescription  = 500
	maxInstructions = 20000 // bounds the heredoc-embedded YAML the Nomad job carries
)

// builtinTools is the exact set the runtime image knows how to toggle.
var builtinTools = []string{"planning", "filesystem"}

// Spec is the strict v1 agent definition parsed from the power user's YAML. Unknown
// keys are rejected (KnownFields) so a typo never silently changes behaviour.
type Spec struct {
	SpecVersion  string     `yaml:"spec_version"`
	Kind         string     `yaml:"kind"`
	Name         string     `yaml:"name"`
	Description  string     `yaml:"description"`
	Instructions string     `yaml:"instructions"`
	LLM          string     `yaml:"llm"`
	Greeting     string     `yaml:"greeting"`
	Tools        Tools      `yaml:"tools"`
	Subagents    []Subagent `yaml:"subagents"`
}

// Tools selects the agent's MCP servers and built-in tools.
type Tools struct {
	MCPServers []MCPServerSel `yaml:"mcp_servers"`
	Builtins   []string       `yaml:"builtins"`
}

// MCPServerSel selects one MCP server for the agent, optionally narrowed to a
// subset of its tools. It accepts two YAML forms so authoring stays terse:
//
//	mcp_servers: [github]                                  # whole server
//	mcp_servers: [{server: github, tools: [get_issue]}]    # a tool subset
//
// An empty Tools list means every tool of the server (no narrowing).
type MCPServerSel struct {
	Server string   `yaml:"server"`
	Tools  []string `yaml:"tools"`
}

// UnmarshalYAML accepts either a bare server name (scalar) or a {server, tools}
// mapping. Unknown mapping keys are rejected to keep the strict-schema contract
// the top-level KnownFields decode cannot reach through a custom unmarshaler.
func (m *MCPServerSel) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		return value.Decode(&m.Server)
	}
	if value.Kind != yaml.MappingNode {
		return fmt.Errorf("mcp_servers entry must be a server name or a {server, tools} mapping: %w", apperr.ErrBadRequest)
	}
	for i := 0; i < len(value.Content); i += 2 {
		if k := value.Content[i].Value; k != "server" && k != "tools" {
			return fmt.Errorf("unknown key %q in mcp_servers entry (allowed: server, tools): %w", k, apperr.ErrBadRequest)
		}
	}
	type raw MCPServerSel
	var r raw
	if err := value.Decode(&r); err != nil {
		return err
	}
	*m = MCPServerSel(r)
	return nil
}

// Names returns just the server names selected (subset info dropped) — used
// where only the set of servers matters.
func (t Tools) Names() []string {
	out := make([]string, 0, len(t.MCPServers))
	for _, m := range t.MCPServers {
		out = append(out, m.Server)
	}
	return out
}

// Subagent is a deepagents-native delegate. LLM is optional (defaults to the
// parent's model in the runtime).
type Subagent struct {
	Name         string `yaml:"name"`
	Description  string `yaml:"description"`
	Instructions string `yaml:"instructions"`
	LLM          string `yaml:"llm"`
}

// Parse strictly decodes agent YAML into a Spec. It rejects unknown keys and any
// line that would break the HCL heredoc / consul-template embedding the job uses.
func Parse(src string) (Spec, error) {
	if err := checkEmbeddingSafe(src); err != nil {
		return Spec{}, err
	}
	dec := yaml.NewDecoder(strings.NewReader(src))
	dec.KnownFields(true)
	var s Spec
	if err := dec.Decode(&s); err != nil {
		return Spec{}, fmt.Errorf("invalid agent YAML: %v: %w", err, apperr.ErrBadRequest)
	}
	return s, nil
}

// Validate checks a parsed Spec against the platform's constraints: field shapes,
// that the model is one the gateway serves (modelKnown), that every selected MCP
// server is deployed in the project, and that any per-server tool subset names a
// tool the server actually exposes. serverTools returns the server's catalog tool
// names and whether the server is deployed (ok=false ⇒ not deployed).
func (s Spec) Validate(modelKnown func(string) bool, serverTools func(string) (names []string, ok bool)) error {
	if s.SpecVersion != "v1" {
		return fmt.Errorf("spec_version must be \"v1\": %w", apperr.ErrBadRequest)
	}
	if s.Kind != "native" {
		return fmt.Errorf("kind must be \"native\": %w", apperr.ErrBadRequest)
	}
	if !nameRE.MatchString(s.Name) {
		return fmt.Errorf("name must be 3-40 chars, lowercase alphanumeric or dashes: %w", apperr.ErrBadRequest)
	}
	if s.Description == "" || len(s.Description) > maxDescription {
		return fmt.Errorf("description is required and must be <=%d chars: %w", maxDescription, apperr.ErrBadRequest)
	}
	if strings.TrimSpace(s.Instructions) == "" || len(s.Instructions) > maxInstructions {
		return fmt.Errorf("instructions are required and must be <=%d chars: %w", maxInstructions, apperr.ErrBadRequest)
	}
	if !modelKnown(s.LLM) {
		return fmt.Errorf("llm %q is not a served model: %w", s.LLM, apperr.ErrBadRequest)
	}
	for _, b := range s.Tools.Builtins {
		if !slices.Contains(builtinTools, b) {
			return fmt.Errorf("unknown builtin tool %q (allowed: planning, filesystem): %w", b, apperr.ErrBadRequest)
		}
	}
	for _, sel := range s.Tools.MCPServers {
		names, ok := serverTools(sel.Server)
		if !ok {
			return fmt.Errorf("mcp server %q is not deployed in this project: %w", sel.Server, apperr.ErrBadRequest)
		}
		for _, tool := range sel.Tools {
			if !slices.Contains(names, tool) {
				return fmt.Errorf("mcp server %q does not expose a tool named %q: %w", sel.Server, tool, apperr.ErrBadRequest)
			}
		}
	}
	for i, sub := range s.Subagents {
		if !nameRE.MatchString(sub.Name) {
			return fmt.Errorf("subagent[%d] name must be 3-40 chars, lowercase alphanumeric or dashes: %w", i, apperr.ErrBadRequest)
		}
		if sub.Description == "" || strings.TrimSpace(sub.Instructions) == "" {
			return fmt.Errorf("subagent %q requires description and instructions: %w", sub.Name, apperr.ErrBadRequest)
		}
		if sub.LLM != "" && !modelKnown(sub.LLM) {
			return fmt.Errorf("subagent %q llm %q is not a served model: %w", sub.Name, sub.LLM, apperr.ErrBadRequest)
		}
	}
	return nil
}

// checkEmbeddingSafe rejects YAML lines that could break out of the Nomad job's
// heredoc or inject a consul-template directive (the job embeds the YAML verbatim).
func checkEmbeddingSafe(src string) error {
	for _, line := range strings.Split(src, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "EOAGENTYAML" {
			return fmt.Errorf("agent YAML must not contain a line equal to the heredoc marker EOAGENTYAML: %w", apperr.ErrBadRequest)
		}
		if strings.Contains(line, "{~{") || strings.Contains(line, "}~}") {
			return fmt.Errorf("agent YAML must not contain the template sentinels {~{ or }~}: %w", apperr.ErrBadRequest)
		}
	}
	return nil
}
