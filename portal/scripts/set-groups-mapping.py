#!/usr/bin/env python3
"""Set the portal Verify app's top-level `attributeMappings` to emit a
multi-valued `groups` claim into the ID token + userinfo.

The portal app was registered manually in the console, whose group dropdown
(`groupIds`/`groupNames`) maps single-valued attributes. The working Boundary/
Nomad apps in this tenant instead set the top-level `attributeMappings` to the
group-membership source `sourceId "4"`, which is multi-valued — that is what
emits ALL of a user's groups as a JSON array. This replicates that, the same way
terraform/infra/modules/identity/scripts/set_app_fields.py does it: GET the full
app object, inject `attributeMappings`, PUT the whole object back (PATCH/POST are
405 on this endpoint). No `function` is set — the portal compares groups
case-insensitively, and a per-value transform risks collapsing the multi-valued
attribute to its first element.

Inputs via env (so the bearer token never lands in argv/ps):
  APP_URL       full app URL, e.g. https://<tenant>/v1.0/applications/<id>
  VERIFY_TOKEN  bootstrap bearer token (client-credentials)
Idempotent: re-running with the same value is a no-op PUT.
"""
import json, os, sys, urllib.request, urllib.error

url, token = os.environ["APP_URL"], os.environ["VERIFY_TOKEN"]
mappings = [{"targetName": "groups", "sourceId": "4"}]
auth = {"Authorization": f"Bearer {token}", "Accept": "application/json"}


def req(method, data=None):
    headers = dict(auth)
    if data is not None:
        headers["Content-Type"] = "application/json"
    with urllib.request.urlopen(urllib.request.Request(url, method=method, data=data, headers=headers)) as f:
        return f.read()


try:
    app = json.loads(req("GET"))
    app["attributeMappings"] = mappings
    req("PUT", json.dumps(app).encode())
except urllib.error.HTTPError as e:
    sys.exit(f"set groups mapping failed: {e.code} {e.read().decode()[:200]}")
print(f"set attributeMappings={mappings}")
