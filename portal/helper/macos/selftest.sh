#!/bin/bash
# Sandbox self-test for secured-ws.sh: stubs `open`/`osascript`, uses a temp HOME,
# and checks parse/validate/upsert. No real terminals or IDEs are launched.
set -u
here="$(cd "$(dirname "$0")" && pwd)"
tmp="$(mktemp -d)"
mkdir -p "$tmp/bin" "$tmp/home"
printf '#!/bin/bash\necho "open $*" >> "%s/calls.log"\n' "$tmp" > "$tmp/bin/open"
printf '#!/bin/bash\necho "osascript" >> "%s/calls.log"\n' "$tmp" > "$tmp/bin/osascript"
# Stub `boundary`: `targets read` exits 0 so ensure_boundary_token treats a token
# as already cached and never attempts a real OIDC login during the test.
printf '#!/bin/bash\necho "boundary $*" >> "%s/calls.log"\nexit 0\n' "$tmp" > "$tmp/bin/boundary"
chmod +x "$tmp/bin/open" "$tmp/bin/osascript" "$tmp/bin/boundary"
run() { HOME="$tmp/home" PATH="$tmp/bin:/usr/bin:/bin" /bin/bash "$here/secured-ws.sh" "$1"; }

echo "== connect (valid) =="
run 'secured-ws://connect?host=ws-ravi-main&user=dev&target_id=tssh_abc123&addr=https%3A%2F%2Fnlb%3A9200&ide=cursor&auth_method_id=amoidc_1234567890'
cat "$tmp/home/.ssh/config"
echo "-- calls: $(cat "$tmp/calls.log")"

echo "== connect again (idempotent) =="
run 'secured-ws://connect?host=ws-ravi-main&user=dev&target_id=tssh_xyz999&addr=https%3A%2F%2Fnlb%3A9200&ide=vscode&auth_method_id=amoidc_1234567890'
echo "Host-block count: $(grep -c '^Host ws-ravi-main$' "$tmp/home/.ssh/config")  target: $(grep -o 'tssh_[A-Za-z0-9]*' "$tmp/home/.ssh/config")"

echo "== disconnect (removes the block) =="
run 'secured-ws://disconnect?host=ws-ravi-main'
if grep -q '^Host ws-ravi-main$' "$tmp/home/.ssh/config"; then echo "FAIL block still present"; else echo "OK block removed"; fi

echo "== disconnect (no-op when absent) =="
run 'secured-ws://disconnect?host=never-connected' && echo "OK no-op" || echo "FAIL errored"

echo "== invalid target_id (must reject) =="
run 'secured-ws://connect?host=h&user=dev&target_id=NOTVALID&addr=https%3A%2F%2Fx&ide=vscode&auth_method_id=amoidc_1234567890' 2>/dev/null \
  && echo "FAIL accepted" || echo "OK rejected"

echo "== missing/invalid auth_method_id (must reject) =="
run 'secured-ws://connect?host=h&user=dev&target_id=tssh_abc123&addr=https%3A%2F%2Fx&ide=vscode&auth_method_id=NOTVALID' 2>/dev/null \
  && echo "FAIL accepted" || echo "OK rejected"

echo "== unknown verb (must reject) =="
run 'secured-ws://evil?x=1' 2>/dev/null && echo "FAIL accepted" || echo "OK rejected"

rm -rf "$tmp"
