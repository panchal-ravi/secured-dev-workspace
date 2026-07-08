package agents

import (
	"errors"
	"strings"
	"testing"

	"github.com/secured-dev-workspace/developer-portal/internal/apperr"
)

const goodYAML = `spec_version: v1
kind: native
name: support-triage
description: Triages incoming support tickets.
instructions: |
  You are a support triage assistant.
llm: deepseek-v4-pro
greeting: "Hi!"
tools:
  mcp_servers: [github]
  builtins: [planning, filesystem]
subagents:
  - name: researcher
    description: digs into docs
    instructions: You research thoroughly.
`

func modelsAre(names ...string) func(string) bool {
	set := map[string]bool{}
	for _, n := range names {
		set[n] = true
	}
	return func(m string) bool { return set[m] }
}

// serversAre builds a serverTools callback where every named server exposes the
// same two tools (search, fetch). An unnamed server reports not-deployed.
func serversAre(names ...string) func(string) ([]string, bool) {
	set := map[string]bool{}
	for _, n := range names {
		set[n] = true
	}
	return func(n string) ([]string, bool) {
		if !set[n] {
			return nil, false
		}
		return []string{"search", "fetch"}, true
	}
}

func TestParseAndValidateGood(t *testing.T) {
	spec, err := Parse(goodYAML)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := spec.Validate(modelsAre("deepseek-v4-pro"), serversAre("github")); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if spec.Name != "support-triage" || len(spec.Subagents) != 1 || len(spec.Tools.Builtins) != 2 {
		t.Fatalf("parsed shape wrong: %+v", spec)
	}
	if got := spec.models(); len(got) != 1 || got[0] != "deepseek-v4-pro" {
		t.Fatalf("models: %v", got)
	}
}

// The subset form {server, tools} parses and validates against the server catalog,
// and coexists with the bare-name form.
func TestParseToolSubsetForm(t *testing.T) {
	src := strings.Replace(goodYAML, "  mcp_servers: [github]", "  mcp_servers:\n    - {server: github, tools: [search]}", 1)
	spec, err := Parse(src)
	if err != nil {
		t.Fatalf("parse subset: %v", err)
	}
	if len(spec.Tools.MCPServers) != 1 || spec.Tools.MCPServers[0].Server != "github" ||
		len(spec.Tools.MCPServers[0].Tools) != 1 || spec.Tools.MCPServers[0].Tools[0] != "search" {
		t.Fatalf("subset not parsed: %+v", spec.Tools.MCPServers)
	}
	if err := spec.Validate(modelsAre("deepseek-v4-pro"), serversAre("github")); err != nil {
		t.Fatalf("validate subset: %v", err)
	}
	// An unknown key inside the mapping is rejected (strict schema through the
	// custom unmarshaler).
	bad := strings.Replace(goodYAML, "  mcp_servers: [github]", "  mcp_servers:\n    - {server: github, oops: x}", 1)
	if _, err := Parse(bad); !errors.Is(err, apperr.ErrBadRequest) {
		t.Fatalf("unknown subset key: want ErrBadRequest, got %v", err)
	}
}

func TestParseUnknownField(t *testing.T) {
	_, err := Parse(goodYAML + "surprise: true\n")
	if !errors.Is(err, apperr.ErrBadRequest) {
		t.Fatalf("unknown field: want ErrBadRequest, got %v", err)
	}
}

func TestValidateRejections(t *testing.T) {
	base := func() Spec {
		s, err := Parse(goodYAML)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		return s
	}
	cases := []struct {
		name   string
		mutate func(*Spec)
		model  func(string) bool
		server func(string) ([]string, bool)
	}{
		{"bad spec_version", func(s *Spec) { s.SpecVersion = "v2" }, modelsAre("deepseek-v4-pro"), serversAre("github")},
		{"bad kind", func(s *Spec) { s.Kind = "remote" }, modelsAre("deepseek-v4-pro"), serversAre("github")},
		{"bad name", func(s *Spec) { s.Name = "A" }, modelsAre("deepseek-v4-pro"), serversAre("github")},
		{"empty description", func(s *Spec) { s.Description = "" }, modelsAre("deepseek-v4-pro"), serversAre("github")},
		{"empty instructions", func(s *Spec) { s.Instructions = "   " }, modelsAre("deepseek-v4-pro"), serversAre("github")},
		{"unknown model", func(s *Spec) {}, modelsAre("other"), serversAre("github")},
		{"unknown builtin", func(s *Spec) { s.Tools.Builtins = []string{"shell"} }, modelsAre("deepseek-v4-pro"), serversAre("github")},
		{"undeployed server", func(s *Spec) {}, modelsAre("deepseek-v4-pro"), serversAre()},
		{"bad subagent model", func(s *Spec) { s.Subagents[0].LLM = "nope" }, modelsAre("deepseek-v4-pro"), serversAre("github")},
		{"unknown subset tool", func(s *Spec) {
			s.Tools.MCPServers = []MCPServerSel{{Server: "github", Tools: []string{"search", "nonexistent"}}}
		}, modelsAre("deepseek-v4-pro"), serversAre("github")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := base()
			c.mutate(&s)
			if err := s.Validate(c.model, c.server); !errors.Is(err, apperr.ErrBadRequest) {
				t.Fatalf("%s: want ErrBadRequest, got %v", c.name, err)
			}
		})
	}
}

// A line equal to the heredoc marker, or containing a template sentinel, is
// rejected before parsing (it could break the job's YAML embedding).
func TestParseRejectsEmbeddingEscapes(t *testing.T) {
	for _, bad := range []string{
		strings.Replace(goodYAML, "  You are a support triage assistant.", "EOAGENTYAML", 1),
		strings.Replace(goodYAML, "  You are a support triage assistant.", "  Use {~{ secret }~}", 1),
	} {
		if _, err := Parse(bad); !errors.Is(err, apperr.ErrBadRequest) {
			t.Fatalf("embedding escape: want ErrBadRequest, got %v", err)
		}
	}
}
