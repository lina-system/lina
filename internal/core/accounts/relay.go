// SPDX-License-Identifier: Apache-2.0

package accounts

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	openAIIssuer       = "https://auth.openai.com"
	openAIResource     = "https://api.openai.com/v1"
	registrationClient = "dynamic_agent_client"
	SignInCallbackPath = "/auth/callback"
	signInLifetime     = 10 * time.Minute
	chatGPTScopes      = "openid profile email offline_access resource.invoke chatgpt.tokens.use.direct"
)

var (
	ErrSignInDenied  = errors.New("sign_in_denied")
	ErrSignInExpired = errors.New("sign_in_expired")
)

// Relayed is an untrusted browser callback, or the URL the person pasted.
// Tokens and the PKCE verifier are never part of the client relay.
type Relayed struct {
	ConnectionID     string `json:"-"` // Trusted connection identity supplied by Core.
	Code             string `json:"code,omitempty"`
	State            string `json:"state,omitempty"`
	Issuer           string `json:"iss,omitempty"`
	ClientID         string `json:"client_id,omitempty"`
	Error            string `json:"error,omitempty"`
	ErrorDescription string `json:"error_description,omitempty"`
	URL              string `json:"url,omitempty"`
}

func (Relayed) Format(s fmt.State, _ rune) { _, _ = io.WriteString(s, "[sign-in callback]") }

type authorization struct {
	url, redirect, state, nonce, verifier string
	clientID, subject                     string
	expires                               time.Time
}

func (authorization) Format(s fmt.State, _ rune) { _, _ = io.WriteString(s, "[pending sign-in]") }

func randomSignInID() string {
	var bytes [32]byte
	_, _ = rand.Read(bytes[:])
	return base64.RawURLEncoding.EncodeToString(bytes[:])
}

func pkceChallenge(verifier string) string {
	digest := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

func newAuthorization(hostID, clientID, subject string, port int, now time.Time) (authorization, error) {
	if strings.TrimSpace(hostID) == "" || port < 1 || port > 65535 || clientID == registrationClient || (clientID == "") != (subject == "") {
		return authorization{}, ErrSignInInvalid
	}
	a := authorization{
		redirect: "http://127.0.0.1:" + strconv.Itoa(port) + SignInCallbackPath,
		state:    randomSignInID(), nonce: randomSignInID(), verifier: randomSignInID(),
		clientID: clientID, subject: subject, expires: now.Add(signInLifetime),
	}
	q := url.Values{
		"client_id": {clientID}, "ext_agent_host_id": {hostID},
		"response_type": {"code"}, "redirect_uri": {a.redirect},
		"scope": {chatGPTScopes}, "resource": {openAIResource},
		"state": {a.state}, "nonce": {a.nonce},
		"code_challenge_method": {"S256"}, "code_challenge": {pkceChallenge(a.verifier)},
	}
	if clientID == "" {
		q.Set("client_id", registrationClient)
		q.Set("agent_name_hint", "LINA")
	}
	a.url = openAIIssuer + "/api/accounts/authorize?" + q.Encode()
	return a, nil
}

func (a authorization) callback(r Relayed, now time.Time) (Relayed, error) {
	if !now.Before(a.expires) {
		return Relayed{}, ErrSignInExpired
	}
	if r.URL != "" {
		if r.Code != "" || r.State != "" || r.Issuer != "" || r.ClientID != "" || r.Error != "" || r.ErrorDescription != "" {
			return Relayed{}, ErrSignInInvalid
		}
		parsed, err := url.Parse(r.URL)
		if err != nil || parsed.User != nil || parsed.Fragment != "" || strings.Contains(r.URL, "#") {
			return Relayed{}, ErrSignInInvalid
		}
		query, err := url.ParseQuery(parsed.RawQuery)
		parsed.RawQuery = ""
		parsed.ForceQuery = false
		if err != nil || parsed.String() != a.redirect {
			return Relayed{}, ErrSignInInvalid
		}
		for name, values := range query {
			if len(values) != 1 {
				return Relayed{}, ErrSignInInvalid
			}
			switch name {
			case "code", "state", "iss", "client_id", "scope", "error", "error_description", "error_uri":
			default:
				return Relayed{}, ErrSignInInvalid
			}
		}
		r = Relayed{Code: query.Get("code"), State: query.Get("state"), Issuer: query.Get("iss"), ClientID: query.Get("client_id"), Error: query.Get("error"), ErrorDescription: query.Get("error_description")}
	}
	if subtle.ConstantTimeCompare([]byte(r.State), []byte(a.state)) != 1 || r.Issuer != "" && r.Issuer != openAIIssuer {
		return Relayed{}, ErrSignInInvalid
	}
	if r.Error != "" {
		if r.Code != "" {
			return Relayed{}, ErrSignInInvalid
		}
		return Relayed{}, ErrSignInDenied
	}
	if r.Code == "" || r.ErrorDescription != "" {
		return Relayed{}, ErrSignInInvalid
	}
	if a.clientID != "" {
		if r.ClientID != "" && r.ClientID != a.clientID {
			return Relayed{}, ErrSignInInvalid
		}
		r.ClientID = a.clientID
	}
	if r.ClientID == "" || r.ClientID == registrationClient {
		return Relayed{}, ErrSignInInvalid
	}
	return r, nil
}
