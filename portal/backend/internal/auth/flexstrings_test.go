package auth

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestFlexStringsUnmarshal(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"array", `["a","b"]`, []string{"a", "b"}},
		{"single string", `"project-acme-developers"`, []string{"project-acme-developers"}},
		{"empty string", `""`, nil},
		{"empty array", `[]`, []string{}},
		{"null", `null`, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var got flexStrings
			if err := json.Unmarshal([]byte(c.in), &got); err != nil {
				t.Fatalf("unmarshal %q: %v", c.in, err)
			}
			if !reflect.DeepEqual([]string(got), c.want) {
				t.Fatalf("got %#v, want %#v", []string(got), c.want)
			}
		})
	}
}
