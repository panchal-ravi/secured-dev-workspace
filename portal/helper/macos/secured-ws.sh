#!/bin/bash
# secured-ws.sh — the logic behind the SecuredWS.app URL handler. It receives one
# secured-ws:// URL (passed by the app's `on open location` handler) and performs
# exactly one of two narrow actions on the developer's own machine, where the
# remote portal backend cannot reach:
#
#   authenticate  -> open a terminal running the one-time Boundary SSO login
#   connect       -> upsert the workspace's SSH config block + open the IDE
#   disconnect    -> remove the workspace's SSH config block (on destroy)
#
# Security: every parameter is validated against a strict pattern and the
# boundary/ssh invocations are rebuilt here from a fixed template — a URL never
# supplies a raw command or a raw ~/.ssh/config block. Nothing is eval'd from the
# URL.
set -euo pipefail

SSH_CONFIG="$HOME/.ssh/config"

# die MSG: surface a native error dialog and stop (the user launched us from a
# browser click, so there is no console to print to).
die() {
  /usr/bin/osascript -e "display dialog \"Secured Workspace helper: $1\" buttons {\"OK\"} default button \"OK\" with icon stop" >/dev/null 2>&1 || true
  echo "secured-ws: $1" >&2
  exit 1
}

# urldecode: turn %XX and + into bytes (printf %b interprets \xNN).
urldecode() { local s="${1//+/ }"; printf '%b' "${s//%/\\x}"; }

# getp KEY -> the (urldecoded) value of the first KEY=... in the query global.
getp() {
  local key="$1" kv IFS='&'
  for kv in $query; do
    [ "${kv%%=*}" = "$key" ] && { urldecode "${kv#*=}"; return; }
  done
}

# esc: escape a string for embedding inside an AppleScript double-quoted literal.
esc() { local s="${1//\\/\\\\}"; printf '%s' "${s//\"/\\\"}"; }

open_terminal() {
  local term="$1" cmd="$2"
  case "$term" in
    iterm)
      /usr/bin/osascript >/dev/null <<OSA
tell application "iTerm"
	activate
	set w to (create window with default profile)
	tell current session of w to write text "$(esc "$cmd")"
end tell
OSA
      ;;
    terminal|"")
      /usr/bin/osascript >/dev/null <<OSA
tell application "Terminal"
	activate
	do script "$(esc "$cmd")"
end tell
OSA
      ;;
    *) die "unsupported terminal '$term'" ;;
  esac
}

# write_ssh_config rebuilds the managed Host block (identical to the portal's
# ProxyCommandConfig) and upserts it between the portal markers, leaving the rest
# of ~/.ssh/config untouched.
write_ssh_config() {
  local host="$1" user="$2" addr="$3" target="$4"
  local begin="# >>> portal:${host} >>>"
  local end="# <<< portal:${host} <<<"
  local block
  block=$(cat <<BLOCK
Host ${host}
    HostName ${host}
    User ${user}
    ProxyCommand sh -c "PATH=/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin BOUNDARY_ADDR=${addr} boundary connect -target-id ${target} -tls-insecure -exec nc -- {{boundary.ip}} {{boundary.port}}"
    StrictHostKeyChecking no
    UserKnownHostsFile /dev/null
    ControlMaster auto
    ControlPath ~/.ssh/cm-%C
    ControlPersist 10m
BLOCK
)
  mkdir -p "$HOME/.ssh"; chmod 700 "$HOME/.ssh"
  local existing="" stripped
  [ -f "$SSH_CONFIG" ] && existing="$(cat "$SSH_CONFIG")"
  # Drop any prior managed block for this host (awk between the two markers).
  stripped="$(printf '%s' "$existing" | awk -v b="$begin" -v e="$end" '
    $0==b {skip=1} skip && $0==e {skip=0; next} !skip {print}')"
  local tmp="${SSH_CONFIG}.securedws.tmp"
  {
    [ -n "$stripped" ] && printf '%s\n' "$stripped"
    printf '%s\n%s\n%s\n' "$begin" "$block" "$end"
  } > "$tmp"
  chmod 600 "$tmp"
  mv "$tmp" "$SSH_CONFIG"
}

# remove_ssh_config strips the managed Host block for this host (between the portal
# markers), leaving the rest of ~/.ssh/config untouched. No-op if the file or block
# is absent, so destroying a workspace that was never connected is harmless.
remove_ssh_config() {
  local host="$1"
  local begin="# >>> portal:${host} >>>"
  local end="# <<< portal:${host} <<<"
  [ -f "$SSH_CONFIG" ] || return 0
  local stripped
  stripped="$(awk -v b="$begin" -v e="$end" '
    $0==b {skip=1} skip && $0==e {skip=0; next} !skip {print}' "$SSH_CONFIG")"
  local tmp="${SSH_CONFIG}.securedws.tmp"
  if [ -n "$stripped" ]; then printf '%s\n' "$stripped" > "$tmp"; else : > "$tmp"; fi
  chmod 600 "$tmp"
  mv "$tmp" "$SSH_CONFIG"
}

open_ide() {
  local ide="$1" host="$2"
  local target="vscode-remote/ssh-remote+${host}/home/dev/project"
  case "$ide" in
    vscode|"")        open "vscode://${target}" ;;
    vscode-insiders)  open "vscode-insiders://${target}" ;;
    cursor)           open "cursor://${target}" ;;
    windsurf)         open "windsurf://${target}" ;;
    *) die "unsupported IDE '$ide'" ;;
  esac
}

# ---- parse ----
url="${1:-}"
[ -n "$url" ] || die "no URL supplied"
[ "${url%%:*}" = "secured-ws" ] || die "unexpected URL scheme"
rest="${url#secured-ws://}"
verb="${rest%%\?*}"
query=""
[ "$rest" != "$verb" ] && query="${rest#*\?}"

# ---- dispatch ----
case "$verb" in
  authenticate)
    addr="$(getp addr)"; amid="$(getp auth_method_id)"; term="$(getp terminal)"
    [[ "$addr" =~ ^https://[A-Za-z0-9._:-]+$ ]] || die "invalid Boundary address"
    [[ "$amid" =~ ^am[a-z]+_[A-Za-z0-9]+$ ]]    || die "invalid auth-method id"
    open_terminal "$term" "BOUNDARY_ADDR=${addr} boundary authenticate oidc -auth-method-id ${amid} -tls-insecure"
    ;;
  connect)
    host="$(getp host)"; user="$(getp user)"; target="$(getp target_id)"
    addr="$(getp addr)"; ide="$(getp ide)"
    [[ "$host" =~ ^[A-Za-z0-9._-]+$ ]]          || die "invalid host"
    [[ "$user" =~ ^[A-Za-z0-9._-]+$ ]]          || die "invalid user"
    [[ "$addr" =~ ^https://[A-Za-z0-9._:-]+$ ]] || die "invalid Boundary address"
    [[ "$target" =~ ^tssh_[A-Za-z0-9]+$ ]]      || die "invalid target id"
    write_ssh_config "$host" "$user" "$addr" "$target"
    open_ide "$ide" "$host"
    ;;
  disconnect)
    host="$(getp host)"
    [[ "$host" =~ ^[A-Za-z0-9._-]+$ ]] || die "invalid host"
    remove_ssh_config "$host"
    ;;
  *) die "unknown action '$verb'" ;;
esac
