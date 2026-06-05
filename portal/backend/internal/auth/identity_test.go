package auth

import "testing"

func TestDeriveIdentity(t *testing.T) {
	cases := []struct {
		email, name         string
		wantHandle, wantGit string
	}{
		{"Ravi.Panchal@ibm.com", "", "ravi-panchal", "ravi-panchal"},
		{"Ravi.Panchal@ibm.com", "Ravi Panchal", "ravi-panchal", "Ravi Panchal"},
		{"alice@example.com", "", "alice", "alice"},
		{"a.b_c@x.io", "", "a-b-c", "a-b-c"},
		{"--weird--@x.io", "", "weird", "weird"},
	}
	for _, c := range cases {
		gotH, gotG := DeriveIdentity(c.email, c.name)
		if gotH != c.wantHandle || gotG != c.wantGit {
			t.Errorf("DeriveIdentity(%q,%q) = (%q,%q), want (%q,%q)", c.email, c.name, gotH, gotG, c.wantHandle, c.wantGit)
		}
	}
}
