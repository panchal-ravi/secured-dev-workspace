package blueprint

import (
	"context"
	"testing"
)

func TestValidate_PassesShapeAndLint(t *testing.T) {
	rv := &recVault{}
	val := NewValidator(NewExecutor(rv, ExecutorConfig{KVMount: "secret"}))
	res, err := val.Validate(context.Background(), validClassC())
	if err != nil {
		t.Fatal(err)
	}
	if !res.Passed {
		t.Fatalf("expected pass, got %+v", res)
	}
	// Validation is static: it must not touch Vault at all.
	if len(rv.ops) != 0 {
		t.Fatalf("validation must make no Vault calls, got: %v", rv.ops)
	}
}

func TestValidate_FailsLint(t *testing.T) {
	rv := &recVault{}
	val := NewValidator(NewExecutor(rv, ExecutorConfig{KVMount: "secret"}))
	bad := validClassC()
	bad.PolicyTpl = `path "sys/mounts" { capabilities = ["read"] }`
	res, err := val.Validate(context.Background(), bad)
	if err != nil {
		t.Fatal(err)
	}
	if res.Passed {
		t.Fatal("a sys/-targeting policy must fail validation")
	}
	if len(rv.ops) != 0 {
		t.Fatalf("lint failure must make no Vault calls, got: %v", rv.ops)
	}
}
