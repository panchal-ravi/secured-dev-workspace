package workspace

import (
	"strings"
	"testing"
)

func TestProxyCommandConfig(t *testing.T) {
	got := ProxyCommandConfig("ws-ravi-main", "dev", "https://nlb:9200", "tssh_abc")
	for _, want := range []string{
		"Host ws-ravi-main",
		"User dev",
		"BOUNDARY_ADDR=https://nlb:9200 boundary connect -target-id tssh_abc -tls-insecure -exec nc",
		"{{boundary.ip}} {{boundary.port}}",
		"ControlPath ~/.ssh/cm-%C", // %% must render to a single %
	} {
		if !strings.Contains(got, want) {
			t.Errorf("config missing %q\n%s", want, got)
		}
	}
}

func TestTransparentConfig(t *testing.T) {
	got := TransparentConfig("main.ravi.project-acme.boundary", "dev")
	if !strings.Contains(got, "Host main.ravi.project-acme.boundary") || !strings.Contains(got, "User dev") {
		t.Errorf("unexpected transparent config:\n%s", got)
	}
}

func TestBoundaryAuthenticateCmd(t *testing.T) {
	got := BoundaryAuthenticateCmd("https://nlb:9200", "amoidc_x")
	want := "BOUNDARY_ADDR=https://nlb:9200 boundary authenticate oidc -auth-method-id amoidc_x -tls-insecure"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}
