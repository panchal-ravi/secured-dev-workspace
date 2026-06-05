package jobrender

import (
	"strings"
	"testing"
)

const sample = `job "${job_name}" {
  namespace = "${namespace}"
  task "workspace" {
    config { image = "${image}" }
    template {
      data = <<EOH
{{ with secret "${ssh_ca_path}" }}{{ .Data.public_key }}{{ end }}
EOH
    }
    env { EMAIL = "${developer_email}" }
  }
}`

func TestRenderFillsPlaceholdersPreservesConsulTemplate(t *testing.T) {
	out, err := Render(sample, map[string]string{
		"job_name":        "ws-ravi-main",
		"namespace":       "project-acme",
		"image":           "you/dev-workspace:poc",
		"ssh_ca_path":     "ssh/project-acme/config/ca",
		"developer_email": "Ravi.Panchal@ibm.com",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, want := range []string{
		`job "ws-ravi-main"`,
		`namespace = "project-acme"`,
		`image = "you/dev-workspace:poc"`,
		`EMAIL = "Ravi.Panchal@ibm.com"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered output missing %q\n%s", want, out)
		}
	}
	// consul-template directives must survive verbatim.
	if !strings.Contains(out, `{{ with secret "ssh/project-acme/config/ca" }}{{ .Data.public_key }}{{ end }}`) {
		t.Errorf("consul-template directive not preserved:\n%s", out)
	}
	if strings.Contains(out, "${") {
		t.Errorf("a ${...} placeholder leaked into output:\n%s", out)
	}
}

func TestRenderErrorsOnMissingValue(t *testing.T) {
	_, err := Render(sample, map[string]string{"job_name": "x"})
	if err == nil {
		t.Fatal("expected error for unsubstituted placeholders, got nil")
	}
	if !strings.Contains(err.Error(), "${namespace}") {
		t.Errorf("error should name the missing placeholder, got: %v", err)
	}
}

func TestRenderDoesNotTouchBareBraces(t *testing.T) {
	out, err := Render(`x = {{ env "NOMAD_ALLOC_ID" }}`, map[string]string{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != `x = {{ env "NOMAD_ALLOC_ID" }}` {
		t.Errorf("bare consul-template mutated: %s", out)
	}
}
