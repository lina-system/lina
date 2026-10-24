// SPDX-License-Identifier: Apache-2.0
package embedding

import (
	"context"
	"encoding/json"
	"github.com/thisisjun786/lina/internal/core/secrets"
	"github.com/thisisjun786/lina/kit/secret"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type vault struct{ t *testing.T }

func (v vault) Get(k secrets.Key) (secret.Secret, error) {
	v.t.Helper()
	if k != (secrets.Key{Person: "owner", Service: "embedding", Name: "test"}) {
		v.t.Fatal(k)
	}
	return secret.New("fixture-key"), nil
}
func TestAdapterMasksOrdersAndAuthenticates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture-key" {
			t.Error("missing key")
		}
		var body struct {
			Model    string
			Input    []string
			Encoding string `json:"encoding_format"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || body.Model != "test" || body.Encoding != "float" || len(body.Input) != 2 {
			t.Error("invalid request")
		}
		if strings.Contains(strings.Join(body.Input, ""), "sk-proj-abcdefghijklmnopqrstuvwx1234567890") {
			t.Error("secret sent")
		}
		if len([]rune(body.Input[1])) != MaxTextRunes {
			t.Error("unbounded input")
		}
		w.Write([]byte(`{"model":"test","data":[{"index":1,"embedding":[0,1]},{"index":0,"embedding":[1,0]}]}`))
	}))
	defer server.Close()
	a, err := New(Config{Endpoint: server.URL, Model: "test", Dimension: 2, KeyName: "test"}, vault{t}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	vectors, err := a.Embed(t.Context(), []string{"key sk-proj-abcdefghijklmnopqrstuvwx1234567890", strings.Repeat("가", MaxTextRunes+1)})
	if err != nil || len(vectors) != 2 || vectors[0][0] != 1 || vectors[1][1] != 1 {
		t.Fatalf("vectors=%v err=%v", vectors, err)
	}
}
func TestAdapterRejectsInvalidResponses(t *testing.T) {
	for _, body := range []string{`{"data":[]}`, `{"data":[{"index":0,"embedding":[1]}]}`, `{"data":[{"index":2,"embedding":[1,0]}]}`, `{"data":[{"index":0,"embedding":[0,0]}]}`, `{"model":"other","data":[{"index":0,"embedding":[1,0]}]}`, `private provider error`} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) }))
			defer server.Close()
			a, err := New(Config{Endpoint: server.URL, Model: "test", Dimension: 2}, nil, "")
			if err != nil {
				t.Fatal(err)
			}
			_, err = a.Embed(context.Background(), []string{"question"})
			if err != ErrUnavailable {
				t.Fatalf("unsanitized error: %v", err)
			}
		})
	}
}
func TestAdapterRefusesRedirectAndCanceledRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://elsewhere.invalid", http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	a, _ := New(Config{Endpoint: server.URL, Model: "test", Dimension: 2}, nil, "")
	if _, err := a.Embed(t.Context(), []string{"question"}); err != ErrUnavailable {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := a.Embed(ctx, []string{"question"}); err != ErrUnavailable {
		t.Fatal(err)
	}
}
