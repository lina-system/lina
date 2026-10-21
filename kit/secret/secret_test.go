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
