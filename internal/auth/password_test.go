package auth

import (
	"errors"
	"strings"
	"testing"
)

func TestPasswordHash(t *testing.T) {
	password := "a long test passphrase"
	first, err := HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	second, err := HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	if first == second || strings.Contains(first, password) {
		t.Fatal("hash must use a fresh salt and hide plaintext")
	}
	for _, test := range []struct {
		name, password string
		want           bool
	}{
		{"correct", password, true}, {"wrong", "another password", false}, {"empty", "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			valid, err := VerifyPassword(first, test.password)
			if err != nil || valid != test.want {
				t.Fatalf("verify = %v, %v", valid, err)
			}
		})
	}
}

func TestPasswordRejectsInvalidInputAndStoredHashes(t *testing.T) {
	for _, password := range []string{"", "short", strings.Repeat("a", 129), string([]byte{0xff})} {
		if _, err := HashPassword(password); !errors.Is(err, ErrInvalidInput) {
			t.Fatal("invalid password accepted")
		}
	}
	valid, err := HashPassword("valid test passphrase")
	if err != nil {
		t.Fatal(err)
	}
	for _, encoded := range []string{"", "plaintext", strings.Replace(valid, "v=19", "v=16", 1),
		strings.Replace(valid, "m=65536", "m=999999999", 1), strings.Replace(valid, "t=3", "t=0", 1),
		strings.Replace(valid, "p=4", "p=999", 1), valid + "$extra", valid[:len(valid)-2]} {
		if _, err := VerifyPassword(encoded, "password"); !errors.Is(err, ErrPasswordHash) {
			t.Fatal("malformed hash accepted")
		}
	}
}

func TestParseCredentials(t *testing.T) {
	credentials, err := ParseCredentials("  Wise.Admin  ", " password with spaces ")
	if err != nil || credentials.username != "wise.admin" || credentials.password != " password with spaces " {
		t.Fatal("normalization changed password or missed username")
	}
	for _, username := range []string{"", "ab", "../admin", "<script>", strings.Repeat("a", 65)} {
		if _, err := ParseCredentials(username, "a passphrase"); !errors.Is(err, ErrInvalidInput) {
			t.Fatal("invalid username accepted")
		}
	}
}

func BenchmarkHashPassword(b *testing.B) {
	for b.Loop() {
		if _, err := HashPassword("benchmark passphrase"); err != nil {
			b.Fatal(err)
		}
	}
}
