// SPDX-License-Identifier: Apache-2.0

package accounts

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"
)

func tokenFixture(t *testing.T) (*rsa.PrivateKey, []byte, map[string]any) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	jwks, _ := json.Marshal(map[string]any{"keys": []any{map[string]any{"kty": "RSA", "kid": "synthetic-key", "use": "sig", "alg": "RS256", "n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())}}})
	claims := map[string]any{"iss": openAIIssuer, "sub": "subject-one", "aud": "oaiapp_test", "nonce": "synthetic-nonce", "iat": int64(1800000000), "exp": int64(1800003600)}
	return key, jwks, claims
}

func signFixture(t *testing.T, key *rsa.PrivateKey, claims map[string]any, alg string) string {
	t.Helper()
	header, _ := json.Marshal(map[string]any{"alg": alg, "kid": "synthetic-key"})
	payload, _ := json.Marshal(claims)
	input := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	sum := sha256.Sum256([]byte(input))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return input + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func TestIDTokenVerifiesSignatureAndIdentityClaims(t *testing.T) {
	key, jwks, claims := tokenFixture(t)
	now := time.Unix(1800000001, 0)
	good := signFixture(t, key, claims, "RS256")
	got, err := verifyIDToken(good, jwks, "oaiapp_test", "synthetic-nonce", now)
	if err != nil || got.Subject != "subject-one" || got.Issuer != openAIIssuer {
		t.Fatalf("valid ID token: %v", err)
	}
	for _, test := range []struct {
		name  string
		field string
		value any
	}{
		{"issuer", "iss", "https://wrong.invalid"}, {"subject", "sub", ""}, {"audience", "aud", "other-client"}, {"nonce", "nonce", "wrong"},
		{"expired", "exp", now.Unix()}, {"future issued", "iat", now.Add(time.Hour).Unix()}, {"not yet valid", "nbf", now.Add(time.Hour).Unix()},
		{"missing expiry", "exp", nil}, {"missing issued", "iat", nil}, {"multiple audiences without azp", "aud", []string{"oaiapp_test", "other-client"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			mutated := map[string]any{}
			for k, v := range claims {
				mutated[k] = v
			}
			if test.value == nil {
				delete(mutated, test.field)
			} else {
				mutated[test.field] = test.value
			}
			if _, err := verifyIDToken(signFixture(t, key, mutated, "RS256"), jwks, "oaiapp_test", "synthetic-nonce", now); !errors.Is(err, ErrSignInIdentity) {
				t.Fatalf("invalid claim accepted: %v", err)
			}
		})
	}
	for _, alg := range []string{"none", "HS256", "RS512"} {
		if _, err := verifyIDToken(signFixture(t, key, claims, alg), jwks, "oaiapp_test", "synthetic-nonce", now); !errors.Is(err, ErrSignInIdentity) {
			t.Fatalf("algorithm %s accepted", alg)
		}
	}
	parts := strings.Split(good, ".")
	claims["sub"] = "tampered"
	payload, _ := json.Marshal(claims)
	parts[1] = base64.RawURLEncoding.EncodeToString(payload)
	if _, err := verifyIDToken(strings.Join(parts, "."), jwks, "oaiapp_test", "synthetic-nonce", now); !errors.Is(err, ErrSignInIdentity) {
		t.Fatal("unsigned identity accepted")
	}
	if _, err := verifyIDToken(good, []byte(`{"keys":[]}`), "oaiapp_test", "synthetic-nonce", now); !errors.Is(err, ErrSignInIdentity) {
		t.Fatal("missing key accepted")
	}
}
