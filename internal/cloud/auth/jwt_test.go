package auth

import (
	"testing"
	"time"
)

// TestMintVerifyJWT tests the full JWT round-trip:
// mint → parse → verify claims.
func TestMintVerifyJWT(t *testing.T) {
	t.Parallel()

	secret := "thisisasecretthatis32byteslong!!"
	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

	claims := JWTClaims{
		Sub:        "user-001",
		Email:      "alice@example.com",
		Name:       "Alice Smith",
		Department: "engineering",
		Role:       "member",
	}

	token, err := MintJWT(secret, claims, now)
	if err != nil {
		t.Fatalf("MintJWT: %v", err)
	}
	if token == "" {
		t.Fatal("MintJWT: returned empty token")
	}

	// Verify within the 7-day window.
	got, err := VerifyJWT(secret, token, now.Add(1*time.Hour))
	if err != nil {
		t.Fatalf("VerifyJWT: %v", err)
	}

	if got.Sub != claims.Sub {
		t.Errorf("Sub: got %q, want %q", got.Sub, claims.Sub)
	}
	if got.Email != claims.Email {
		t.Errorf("Email: got %q, want %q", got.Email, claims.Email)
	}
	if got.Name != claims.Name {
		t.Errorf("Name: got %q, want %q", got.Name, claims.Name)
	}
	if got.Department != claims.Department {
		t.Errorf("Department: got %q, want %q", got.Department, claims.Department)
	}
	if got.Role != claims.Role {
		t.Errorf("Role: got %q, want %q", got.Role, claims.Role)
	}

	// iat = now, exp = now + 7 days (604800 seconds).
	wantIat := now.Unix()
	wantExp := now.Unix() + 604800
	if got.Iat != wantIat {
		t.Errorf("Iat: got %d, want %d", got.Iat, wantIat)
	}
	if got.Exp != wantExp {
		t.Errorf("Exp: got %d, want %d", got.Exp, wantExp)
	}
}

func TestMintJWT_SecretTooShort(t *testing.T) {
	t.Parallel()
	_, err := MintJWT("short", JWTClaims{}, time.Now())
	if err == nil {
		t.Fatal("expected error for short secret, got nil")
	}
}

func TestVerifyJWT_Expired(t *testing.T) {
	t.Parallel()

	secret := "thisisasecretthatis32byteslong!!"
	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

	token, err := MintJWT(secret, JWTClaims{Sub: "u1", Email: "x@y.com"}, now)
	if err != nil {
		t.Fatalf("MintJWT: %v", err)
	}

	// Verify 8 days later — past the 7-day expiry.
	_, err = VerifyJWT(secret, token, now.Add(8*24*time.Hour))
	if err == nil {
		t.Fatal("expected expired error, got nil")
	}
}

func TestVerifyJWT_WrongSecret(t *testing.T) {
	t.Parallel()

	secret := "thisisasecretthatis32byteslong!!"
	other := "differentssecretthatis32byteslong"
	now := time.Now()

	token, err := MintJWT(secret, JWTClaims{Sub: "u1", Email: "x@y.com"}, now)
	if err != nil {
		t.Fatalf("MintJWT: %v", err)
	}

	_, err = VerifyJWT(other, token, now)
	if err == nil {
		t.Fatal("expected signature error for wrong secret, got nil")
	}
}

func TestVerifyJWT_Malformed(t *testing.T) {
	t.Parallel()

	secret := "thisisasecretthatis32byteslong!!"
	cases := []struct {
		name  string
		token string
	}{
		{"empty", ""},
		{"one part", "abc"},
		{"two parts", "abc.def"},
		{"garbage", "!!!.!!.!!"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := VerifyJWT(secret, tc.token, time.Now())
			if err == nil {
				t.Errorf("expected error for malformed token %q, got nil", tc.token)
			}
		})
	}
}
