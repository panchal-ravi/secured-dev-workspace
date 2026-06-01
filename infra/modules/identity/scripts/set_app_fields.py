#!/usr/bin/env python3
"""Set the app-level fields Verify won't accept via PATCH/POST, in one PUT.

Two fields need this treatment, and both are app-level (not settable with
PATCH/POST on the app endpoint — both return 405), so the only method that works
is GET the full app object, inject the fields, and PUT the whole object back:

  - providers.saml.properties.companyName  -> the displayed "Company Name"
    (lives under the auto-created `saml` block even for OIDC apps).
  - top-level `attributeMappings`          -> the "ID token and user info"
    attribute mapping. This is what emits the `groups` claim into the ID token
    and userinfo (the ONLY places Boundary/Nomad read claims). It is SEPARATE
    from providers.oidc.token.attributeMappings, which only shapes the JWT
    access token that neither relying party inspects.

Both are set in a single GET->PUT so the two writes can't race and clobber each
other (two independent PUTs to the same object would lose-update). Round-tripping
the full object preserves the generated clientId/clientSecret and every other
field.

Inputs via env (so the bearer token never lands in argv/ps):
  APP_URL        full URL of the app, e.g. https://<tenant>/v1.0/applications/<id>
  VERIFY_TOKEN   bootstrap bearer token
  COMPANY_NAME   value for companyName
  ATTR_MAPPINGS  JSON array for the top-level attributeMappings
Idempotent: re-running with the same values is a no-op PUT. Exits non-zero on any
HTTP error so the Terraform apply fails loudly.
"""
import json, os, sys, urllib.request, urllib.error

url, token = os.environ["APP_URL"], os.environ["VERIFY_TOKEN"]
company, attr_mappings = os.environ["COMPANY_NAME"], json.loads(os.environ["ATTR_MAPPINGS"])
auth = {"Authorization": f"Bearer {token}", "Accept": "application/json"}


def req(method, data=None):
    headers = dict(auth)
    if data is not None:
        headers["Content-Type"] = "application/json"
    with urllib.request.urlopen(urllib.request.Request(url, method=method, data=data, headers=headers)) as f:
        return f.read()


try:
    app = json.loads(req("GET"))
    app.setdefault("providers", {}).setdefault("saml", {}).setdefault("properties", {})["companyName"] = company
    app["attributeMappings"] = attr_mappings
    req("PUT", json.dumps(app).encode())
except urllib.error.HTTPError as e:
    sys.exit(f"set app fields failed: {e.code} {e.read().decode()[:200]}")
print(f"set companyName={company!r}, attributeMappings={[m.get('targetName') for m in attr_mappings]}")
