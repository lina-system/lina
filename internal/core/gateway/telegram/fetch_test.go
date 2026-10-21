// SPDX-License-Identifier: Apache-2.0

package telegram

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/kit/secret"
)

func TestFetchUsesOnlyTelegramFilePath(t *testing.T) {
	for _, path := range []string{"documents/file_1.txt", "../private", "https://example.invalid/file", "documents/../file", "documents/file?token=x", "documents/%2e%2e/file", "documents/file#fragment", "/absolute"} {
		t.Run(path, func(t *testing.T) {
			calls := 0
			c, _ := newClient(secret.New(testToken), roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if calls == 1 {
					io.Copy(io.Discard, r.Body)
					return response(r, 200, `{"ok":true,"result":{"file_id":"file-id","file_path":"`+path+`"}}`), nil
				}
				if r.URL.Host != "api.telegram.org" || r.URL.Path != "/file/bot"+testToken+"/documents/file_1.txt" {
					t.Fatal("untrusted file destination")
				}
				return response(r, 200, "file contents"), nil
			}))
			body, err := c.fetch(t.Context(), "file-id")
			if path != "documents/file_1.txt" {
				if err == nil || calls != 1 {
					t.Fatalf("unsafe fetch: err=%v calls=%d", err, calls)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer body.Close()
			data, err := io.ReadAll(body)
			if err != nil || string(data) != "file contents" || calls != 2 {
				t.Fatalf("fetch: %v calls=%d", err, calls)
			}
		})
	}
}

type leakingBody struct{}

func (leakingBody) Read([]byte) (int, error) { return 0, &credentialError{} }
func (leakingBody) Close() error             { return &credentialError{} }

type credentialError struct{}

func (*credentialError) Error() string { return testToken }

func TestFileReadErrorsCannotLeakToken(t *testing.T) {
	calls := 0
	c, _ := newClient(secret.New(testToken), roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			io.Copy(io.Discard, r.Body)
			return response(r, 200, `{"ok":true,"result":{"file_id":"file-id","file_path":"documents/file.txt"}}`), nil
		}
		res := response(r, 200, "")
		res.Body = leakingBody{}
		return res, nil
	}))
	body, err := c.fetch(t.Context(), "file-id")
	if err != nil {
		t.Fatal(err)
	}
	_, readErr := io.ReadAll(body)
	closeErr := body.Close()
	for _, err := range []error{readErr, closeErr} {
		if err == nil || strings.Contains(err.Error(), testToken) {
			t.Fatalf("unsanitized file error: %v", err)
		}
	}
}
