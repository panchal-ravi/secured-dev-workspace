"""Tests for the file-loaded scope authorization gate."""
import json

import jwt
import pytest

from exceptions.errors import VerifyAuthorizationError
from verify.authorization import authorize_scope, load_scope_policy, set_scope_policy


def _token(groups=None) -> str:
    payload: dict = {"sub": "user"}
    if groups is not None:
        payload["groups"] = groups
    return jwt.encode(payload, "secret", algorithm="HS256")


# Policy used by the authorize_scope cases below (overrides the conftest demo
# policy via the autouse fixture). Group casing is mixed deliberately to prove
# the policy side is lowercased when installed through load_scope_policy.
_RECORDS_POLICY = {
    "records.read": frozenset({"acme-operators", "acme-admins"}),
    "records.write": frozenset({"acme-admins"}),
}


@pytest.fixture(autouse=True)
def _records_policy():
    set_scope_policy(dict(_RECORDS_POLICY))
    yield


class TestAuthorizeScope:
    @pytest.mark.parametrize(
        "groups, scope",
        [
            (["acme-admins"], "records.write"),                  # array, exact
            (["acme-operators"], "records.read"),                # array, alt group
            (["acme-operators", "acme-admins"], "records.read records.write"),  # multi-scope
            ("acme-admins", "records.write"),                    # scalar string
            ("acme-operators,acme-admins", "records.read"),      # comma string
            (["ACME-ADMINS"], "records.write"),                  # case-insensitive (token side)
        ],
    )
    def test_entitled(self, groups, scope):
        authorize_scope(_token(groups), scope)  # must not raise

    @pytest.mark.parametrize(
        "groups, scope",
        [
            (["acme-operators"], "records.write"),                  # disjoint groups
            (["acme-operators"], "records.read records.write"),     # partial in multi-scope
            (["acme-admins"], "records.delete"),                    # unknown scope
            (None, "records.read"),                                 # missing groups claim
            ([], "records.read"),                                   # empty groups list
            (123, "records.read"),                                  # malformed groups type
        ],
    )
    def test_denied(self, groups, scope):
        with pytest.raises(VerifyAuthorizationError):
            authorize_scope(_token(groups), scope)

    def test_case_insensitive_on_both_sides(self):
        # Policy group is upper-cased on install path via load_scope_policy
        # (lowercased), and the token group is mixed-case — they still match.
        set_scope_policy({"records.read": frozenset({"acme-admins"})})
        authorize_scope(_token(["Acme-Admins"]), "records.read")

    def test_empty_policy_denies_everything(self):
        set_scope_policy({})
        with pytest.raises(VerifyAuthorizationError):
            authorize_scope(_token(["acme-admins"]), "records.read")


class TestLoadScopePolicy:
    def test_loads_and_lowercases_groups(self, tmp_path):
        path = tmp_path / "policy.json"
        path.write_text(json.dumps({"records.read": ["Acme-Admins", "ACME-OPERATORS"]}))

        policy = load_scope_policy(str(path))

        assert policy == {"records.read": frozenset({"acme-admins", "acme-operators"})}

    def test_missing_file_raises(self, tmp_path):
        # Acceptance: policy file absent => boot fails (this is what main.py
        # calls during lifespan startup).
        with pytest.raises(FileNotFoundError):
            load_scope_policy(str(tmp_path / "does-not-exist.json"))

    def test_malformed_json_raises(self, tmp_path):
        path = tmp_path / "bad.json"
        path.write_text("{ not valid json")
        with pytest.raises(json.JSONDecodeError):
            load_scope_policy(str(path))

    def test_non_object_root_raises(self, tmp_path):
        path = tmp_path / "arr.json"
        path.write_text(json.dumps(["records.read"]))
        with pytest.raises(ValueError):
            load_scope_policy(str(path))

    def test_bad_entry_shape_raises(self, tmp_path):
        path = tmp_path / "shape.json"
        path.write_text(json.dumps({"records.read": "acme-admins"}))  # value must be a list
        with pytest.raises(ValueError):
            load_scope_policy(str(path))

    def test_empty_object_is_valid_and_denies(self, tmp_path):
        path = tmp_path / "empty.json"
        path.write_text(json.dumps({}))

        policy = load_scope_policy(str(path))
        assert policy == {}

        set_scope_policy(policy)
        with pytest.raises(VerifyAuthorizationError):
            authorize_scope(_token(["acme-admins"]), "records.read")
