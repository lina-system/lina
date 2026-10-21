// SPDX-License-Identifier: Apache-2.0

package accounts

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"strings"
	"time"
)

var ErrSignInIdentity = errors.New("sign_in_identity_invalid")

type verifiedIdentity struct{ Issuer, Subject string }

// OpenAI's published discovery document advertises RS256. No token-controlled
// algorithm, key URL, embedded key, or unverified identity selects a trust root.
func verifyIDToken(raw string, jwks []byte, clientID, nonce string, now time.Time) (verifiedIdentity, error) {
	invalid := func() (verifiedIdentity, error) { return verifiedIdentity{}, ErrSignInIdentity }
	if len(raw) > 64<<10 || clientID == "" || nonce == "" {
		return invalid()
	}
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return invalid()
	}
	var header struct {
		Algorithm string   `json:"alg"`
		KeyID     string   `json:"kid"`
		Critical  []string `json:"crit"`
	}
	h, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || json.Unmarshal(h, &header) != nil || header.Algorithm != "RS256" || header.KeyID == "" || len(header.Critical) != 0 {
		return invalid()
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return invalid()
	}
	var keys struct {
		Keys []struct {
			Type      string `json:"kty"`
			ID        string `json:"kid"`
			Use       string `json:"use"`
			Algorithm string `json:"alg"`
			N         string `json:"n"`
			E         string `json:"e"`
		} `json:"keys"`
	}
	if len(jwks) > 128<<10 || json.Unmarshal(jwks, &keys) != nil {
		return invalid()
	}
	var public *rsa.PublicKey
	for _, key := range keys.Keys {
		if key.ID != header.KeyID {
			continue
		}
		if public != nil || key.Type != "RSA" || key.Use != "" && key.Use != "sig" || key.Algorithm != "" && key.Algorithm != "RS256" {
			return invalid()
		}
		n, nerr := base64.RawURLEncoding.DecodeString(key.N)
		e, eerr := base64.RawURLEncoding.DecodeString(key.E)
		exponent := new(big.Int).SetBytes(e)
		modulus := new(big.Int).SetBytes(n)
		if nerr != nil || eerr != nil || modulus.BitLen() < 2048 || modulus.BitLen() > 8192 || !exponent.IsInt64() || exponent.Int64() < 3 || exponent.Int64() > 1<<31-1 || exponent.Bit(0) == 0 {
			return invalid()
		}
		public = &rsa.PublicKey{N: modulus, E: int(exponent.Int64())}
	}
	if public == nil {
		return invalid()
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(public, crypto.SHA256, digest[:], signature) != nil {
		return invalid()
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return invalid()
	}
	var claims struct {
		Issuer          string          `json:"iss"`
		Subject         string          `json:"sub"`
		Audience        json.RawMessage `json:"aud"`
		AuthorizedParty string          `json:"azp"`
		Nonce           string          `json:"nonce"`
		Expires         *int64          `json:"exp"`
		Issued          *int64          `json:"iat"`
		NotBefore       *int64          `json:"nbf"`
	}
	if json.Unmarshal(payload, &claims) != nil || claims.Issuer != openAIIssuer || strings.TrimSpace(claims.Subject) == "" ||
		claims.Expires == nil || *claims.Expires <= now.Unix() || claims.Issued == nil || *claims.Issued > now.Unix()+5 || *claims.Issued >= *claims.Expires ||
		claims.NotBefore != nil && *claims.NotBefore > now.Unix()+5 || subtle.ConstantTimeCompare([]byte(claims.Nonce), []byte(nonce)) != 1 {
		return invalid()
	}
	var single string
	var audiences []string
	if json.Unmarshal(claims.Audience, &single) == nil {
		audiences = []string{single}
	} else if json.Unmarshal(claims.Audience, &audiences) != nil {
		return invalid()
	}
	found := false
	for _, aud := range audiences {
		if aud == clientID {
			found = true
		}
	}
	if !found || len(audiences) > 1 && claims.AuthorizedParty != clientID || claims.AuthorizedParty != "" && claims.AuthorizedParty != clientID {
		return invalid()
	}
	return verifiedIdentity{Issuer: claims.Issuer, Subject: claims.Subject}, nil
}
