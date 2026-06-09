package workspace

import "fmt"

// ProxyCommandConfig builds the non-transparent SSH config block: each
// connection runs `boundary connect ... -exec nc` so there is no Client Agent
// daemon and no DNS interception (better throughput). Boundary's worker injects
// the Vault-signed cert, so there is deliberately no IdentityFile.
//
// The ProxyCommand sets a fixed PATH containing the usual Boundary install dirs
// plus the base system dirs (for `nc`), because VSCode Remote-SSH (launched from
// the macOS Dock) spawns ssh with the launchd default PATH, which omits
// /usr/local/bin and /opt/homebrew/bin — so a bare `boundary` would be "command
// not found". We deliberately do NOT append the inherited $PATH: ssh runs the
// ProxyCommand through /bin/sh, which expands $PATH and re-parses it inside the
// inner `sh -c`, so a PATH entry containing a space (e.g. ".../VMware Fusion.app/...")
// splits the line and the proxy dies with "File name too long". A fixed,
// space-free PATH that locates `boundary` + `nc` is all this command needs.
func ProxyCommandConfig(hostLabel, user, boundaryAddr, targetID string) string {
	return fmt.Sprintf(`Host %[1]s
    HostName %[1]s
    User %[2]s
    ProxyCommand sh -c "PATH=/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin BOUNDARY_ADDR=%[3]s boundary connect -target-id %[4]s -tls-insecure -exec nc -- {{boundary.ip}} {{boundary.port}}"
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
