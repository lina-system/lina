// SPDX-License-Identifier: Apache-2.0

package accounts

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/kit/provider"
	"github.com/thisisjun786/lina/kit/secret"
)

func TestChatGPTRefreshUsesSavedClientAndPersistsGrantedScopes(t *testing.T) {
	calls := 0
	refresh := ChatGPTRefresher(signInLeaser{}, signInTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.String() != openAIIssuer+"/api/accounts/oauth/token" || r.Method != http.MethodPost || r.Header.Get("Authorization") != "" || r.Header.Get("X-Lina-Refresh") != "" {
			t.Fatal("refresh credential in wrong destination or header")
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if len(r.Form) != 4 || r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("client_id") != "client" || r.Form.Get("refresh_token") != "synthetic-refresh" || r.Form.Get("resource") != openAIResource {
			t.Fatal("refresh grant changed saved registration")
		}
		return wireResponse(200, `{"access_token":"access-next","refresh_token":"refresh-next","token_type":"Bearer","scope":"openid chatgpt.tokens.use.direct","expires_in":3600}`), nil
	}))
	r, err := refresh(t.Context(), account("one"), secret.New("synthetic-refresh"))
	if err != nil || calls != 1 || len(r.Scopes) != 2 {
		t.Fatalf("refresh: calls=%d error=%v", calls, err)
	}
	if got := bearer(t, r.Tokens.Credential("one", provider.LoopbackOAuth)); got != "Bearer access-next" {
		t.Fatal("wrong rotated access credential")
	}
	f := setup(t, nil)
	add(t, f, "one")
	if err := f.s.Replace(t.Context(), "one", r); err != nil {
		t.Fatal(err)
	}
	saved, err := f.s.Get(t.Context(), "one")
	if err != nil || len(saved.Scopes) != 2 || saved.Scopes[0] != "openid" {
		t.Fatalf("granted scopes not stored: %v", err)
	}
}

func TestChatGPTRefreshRejectsRedirectsAndLostPlanPermission(t *testing.T) {
	for _, status := range []int{200, 307, 400} {
		calls := 0
		refresh := ChatGPTRefresher(signInLeaser{}, signInTransport(func(*http.Request) (*http.Response, error) {
			calls++
			r := wireResponse(status, `{"access_token":"secret-access","refresh_token":"secret-refresh","token_type":"Bearer","scope":"openid","expires_in":3600,"error":"invalid_grant","error_description":"secret-refresh"}`)
			r.Header.Set("Location", "https://wrong.invalid/token")
			return r, nil
		}))
		_, err := refresh(t.Context(), account("one"), secret.New("secret-refresh"))
		if err == nil || calls != 1 || strings.Contains(err.Error(), "secret-refresh") {
			t.Fatalf("invalid refresh: %v calls=%d", err, calls)
		}
		if status == 200 && !errors.Is(err, ErrSignInScope) {
			t.Fatalf("scope refusal: %v", err)
		}
	}
}
