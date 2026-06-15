package blueprint

import (
	"context"
	"testing"
)

func TestValidate_PassesWhenAllowedReads200AndDeniedIs403(t *testing.T) {
	rv := &recVault{}                       // Read returns ok=true (allowed read 200)
	val := NewValidator(rv, NewExecutor(rv, ExecutorConfig{}))
	res, err := val.Validate(context.Background(), validClassC())
	if err != nil {
		t.Fatal(err)
	}
	if !res.Passed {
		t.Fatalf("expected pass, got %+v", res)
	}
	// the throwaway namespace must be created and then deleted
	if idx(rv.ops, "ns+") == (1<<30) || idx(rv.ops, "ns-") == (1<<30) {
		t.Fatalf("validator must create and delete a throwaway namespace: %v", rv.ops)
	}
	if idx(rv.ops, "ns-") < idx(rv.ops, "ns+") {
		t.Fatal("delete must come after create")
	}
}

func TestValidate_FailsLintBeforeTouchingVault(t *testing.T) {
	rv := &recVault{}
	val := NewValidator(rv, NewExecutor(rv, ExecutorConfig{}))
	bad := validClassC()
	bad.PolicyTpl = `path "sys/mounts" { capabilities = ["read"] }`
	res, err := val.Validate(context.Background(), bad)
	if err != nil {
		t.Fatal(err)
	}
	if res.Passed {
		t.Fatal("a sys/-targeting policy must fail validation")
	}
	if contains(rv.ops, "ns+") {
		t.Fatal("lint must fail before creating a throwaway namespace")
	}
}
