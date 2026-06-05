package workspace

import "fmt"

// ProxyCommandConfig builds the non-transparent SSH config block: each
// connection runs `boundary connect ... -exec nc` so there is no Client Agent
// daemon and no DNS interception (better throughput). Boundary's worker injects
// the Vault-signed cert, so there is deliberately no IdentityFile.
//
// The ProxyCommand prepends the usual Boundary install dirs to PATH because
// VSCode Remote-SSH (launched from the macOS Dock) spawns ssh with the launchd
// default PATH (/usr/bin:/bin:/usr/sbin:/sbin), which omits /usr/local/bin and
// /opt/homebrew/bin — so a bare `boundary` would be "command not found" and the
// connection would fail even though it works from a terminal shell.
func ProxyCommandConfig(hostLabel, user, boundaryAddr, targetID string) string {
	return fmt.Sprintf(`Host %[1]s
    HostName %[1]s
    User %[2]s
    ProxyCommand sh -c "PATH=/usr/local/bin:/opt/homebrew/bin:$PATH BOUNDARY_ADDR=%[3]s boundary connect -target-id %[4]s -tls-insecure -exec nc -- {{boundary.ip}} {{boundary.port}}"
    StrictHostKeyChecking no
    UserKnownHostsFile /dev/null
    ControlMaster auto
    ControlPath ~/.ssh/cm-%%C
    ControlPersist 10m
`, hostLabel, user, boundaryAddr, targetID)
}

// TransparentConfig builds the alias-based SSH config block, which requires the
// Boundary Client Agent (it intercepts DNS for the alias and injects the cert).
func TransparentConfig(alias, user string) string {
	return fmt.Sprintf(`Host %[1]s
    User %[2]s
`, alias, user)
}

// BoundaryAuthenticateCmd is the one-time-per-session SSO login the developer
// runs before connecting (both connection methods need a Boundary token).
func BoundaryAuthenticateCmd(boundaryAddr, oidcAuthMethodID string) string {
	return fmt.Sprintf("BOUNDARY_ADDR=%s boundary authenticate oidc -auth-method-id %s -tls-insecure", boundaryAddr, oidcAuthMethodID)
}
