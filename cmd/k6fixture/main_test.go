package main

import "testing"

func TestAccountsComeFromUserColonPassword(t *testing.T) {
	a, err := account(" editor:pa:ss ")
	if err != nil || a.Username != "editor" || a.Password != "pa:ss" {
		t.Fatalf("account = %+v, %v", a, err)
	}
	for _, bad := range []string{"", "editor", ":pass", "editor:"} {
		if _, err := account(bad); err == nil {
			t.Fatalf("account(%q) accepted", bad)
		}
	}
	if got := split(" a, ,b "); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("split = %v", got)
	}
	if err := run(nil); err == nil {
		t.Fatal("missing command accepted")
	}
	if err := run([]string{"reset"}); err == nil {
		t.Fatal("reset without a target accepted")
	}
}
