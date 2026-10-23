// SPDX-License-Identifier: Apache-2.0
package secret_test

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/kit/secret"
)

const token = "synthetic-credential-48"

type credentialRecord struct{ Value secret.Secret }

func TestSecretFormatting(t *testing.T) {
	value := secret.New(token)
	for _, format := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%X", "%d", "%020s", "%.4s"} {
		for _, v := range []any{value, &value, secret.Secret{}} {
			if got := fmt.Sprintf(format, v); got != secret.Placeholder {
				t.Fatalf("format %s: %s", format, got)
			}
		}
	}
	for _, v := range []any{struct{ Value secret.Secret }{value}, []secret.Secret{value}, map[string]secret.Secret{"key": value}, struct{ hidden secret.Secret }{value}} {
		for _, format := range []string{"%v", "%+v", "%#v"} {
			if strings.Contains(fmt.Sprintf(format, v), token) {
				t.Fatalf("nested format %s exposed credential", format)
			}
		}
	}
	for _, marshal := range []func(any) ([]byte, error){json.Marshal, xml.Marshal} {
		b, err := marshal(credentialRecord{value})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), token) || !bytes.Contains(b, []byte(secret.Placeholder)) {
			t.Fatalf("unsafe serialization: %s", b)
		}
	}
	var log bytes.Buffer
	slog.New(slog.NewJSONHandler(&log, nil)).Info("credential", "value", value)
	if strings.Contains(log.String(), token) || !strings.Contains(log.String(), secret.Placeholder) {
		t.Fatal("unsafe log")
	}
}

func TestCredentialInjection(t *testing.T) {
	value := secret.New(token)
	env, err := secret.Environment("SERVICE_TOKEN", value)
	if err != nil || env != "SERVICE_TOKEN="+token {
		t.Fatal("environment injection failed")
	}
	headers := http.Header{}
	if err := secret.InjectHeader(headers, "Authorization", "Bearer ", value); err != nil {
		t.Fatal(err)
	}
	if headers.Get("Authorization") != "Bearer "+token {
		t.Fatal("header injection failed")
	}
	for _, name := range []string{"", "A=B", "A\x00B"} {
		if _, err := secret.Environment(name, value); err == nil {
			t.Fatal("invalid environment name accepted")
		}
	}
	for _, value := range []secret.Secret{secret.New("bad\r\nvalue"), secret.Secret{}} {
		if err := secret.InjectHeader(headers, "Authorization", "Bearer ", value); err == nil {
			t.Fatal("invalid header value accepted")
		}
	}
	if err := secret.InjectHeader(headers, "Bad Name", "", value); err == nil {
		t.Fatal("invalid header name accepted")
	}
	if err := secret.InjectHeader(nil, "Authorization", "", value); err == nil {
		t.Fatal("nil header accepted")
	}
}

// murmur masks only the userinfo password and the values of sensitive query or
// fragment names inside a URL; hosts, paths, names and other values stay.
// Research: docs/research/messengers.md (murmur mapped onto Core).
func TestMaskURLSecrets(t *testing.T) {
	const hash = "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"
	for _, tc := range []struct{ name, input, want string }{
		{"query token", "link https://example.com/cb?token=synthetic-abc123&next=/home", "link https://example.com/cb?token=" + secret.Placeholder + "&next=/home"},
		{"encoded name", "https://example.com/x?%74oken=synthetic-abc", "https://example.com/x?%74oken=" + secret.Placeholder},
		{"suffix name", "https://example.com/x?access_token=synthetic-abc", "https://example.com/x?access_token=" + secret.Placeholder},
		{"signature name", "https://example.com/x?sig=synthetic-abc123", "https://example.com/x?sig=" + secret.Placeholder},
		{"session name", "https://example.com/x?session=synthetic-abc123", "https://example.com/x?session=" + secret.Placeholder},
		{"fragment name", "https://example.com/#refresh_token=synthetic-abc", "https://example.com/#refresh_token=" + secret.Placeholder},
		{"userinfo password", "https://jun:synthetic-pass@example.com/path", "https://jun:" + secret.Placeholder + "@example.com/path"},
		{"plain url", "https://example.com/a/b?page=2&sort=asc#top", "https://example.com/a/b?page=2&sort=asc#top"},
		{"id and hash", "https://example.com/items/12345 and sha256 " + hash, "https://example.com/items/12345 and sha256 " + hash},
		{"hash value", "https://example.com/x?id=" + hash, "https://example.com/x?id=" + hash},
		{"trailing punctuation", "https://example.com/x?token=synthetic-abc).", "https://example.com/x?token=" + secret.Placeholder + ")."},
		{"two pairs", "https://example.com/x?a=1&sig=synthetic-1&b=2", "https://example.com/x?a=1&sig=" + secret.Placeholder + "&b=2"},
		{"empty value", "https://example.com/x?token=&a=1", "https://example.com/x?token=&a=1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			masked, err := secret.Mask(tc.input)
			if err != nil {
				t.Fatal(err)
			}
			if masked.Text() != tc.want {
				t.Fatalf("masked = %q, want %q", masked.Text(), tc.want)
			}
			if strings.Contains(masked.DerivedText(), secret.Placeholder) {
				t.Fatal("derived text retained a placeholder")
			}
		})
	}
}
