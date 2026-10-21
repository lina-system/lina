// SPDX-License-Identifier: Apache-2.0
package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestUsageUnknownRoundTrip(t *testing.T) {
	var u Usage
	b, err := json.Marshal(u)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(b, &fields); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"input", "cached", "cache_write", "output", "reasoning", "cost_estimate"} {
		if v, ok := fields[key]; !ok || v != nil {
			t.Fatalf("%s = %v, present %v", key, v, ok)
		}
	}
	zero := int64(0)
	u.Input = &zero
	b, _ = json.Marshal(u)
	var got Usage
	if err := json.Unmarshal(b, &got); err != nil || got.Input == nil || *got.Input != 0 || got.Output != nil {
		t.Fatalf("lost known zero: %s, %v", b, err)
	}
}

func TestFailureVocabulary(t *testing.T) {
	for _, kind := range []FailureKind{NeedsSignIn, NotEligible, UsageCapped, Unavailable, NotAccepted} {
		b, err := json.Marshal(kind)
		if err != nil {
			t.Fatal(err)
		}
		var got FailureKind
		if err := json.Unmarshal(b, &got); err != nil || got != kind {
			t.Fatalf("round trip %s: %v", b, err)
		}
		if (&Failure{Kind: kind}).Error() != string(kind) {
			t.Fatal("failure must expose only its closed code")
		}
	}
	for _, bad := range []string{`"unknown"`, `""`, `null`, `7`} {
		var k FailureKind
		if json.Unmarshal([]byte(bad), &k) == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
	if _, err := json.Marshal(FailureKind("arbitrary")); err == nil {
		t.Fatal("marshaled invalid failure")
	}
}

func TestCredentialCannotPrintOrMarshalBearer(t *testing.T) {
	c := NewCredential("account", LoopbackOAuth, "synthetic-private-token")
	b, _ := json.Marshal(c)
	for _, text := range []string{string(b), fmt.Sprint(c), fmt.Sprintf("%+v", c), fmt.Sprintf("%#v", c)} {
		if strings.Contains(text, "synthetic-private-token") {
			t.Fatal("credential leaked")
		}
	}
	req, _ := http.NewRequest("POST", "https://api.openai.com/v1/responses", nil)
	if err := c.Authorize(req); err != nil || req.Header.Get("Authorization") != "Bearer synthetic-private-token" {
		t.Fatal("missing bearer", err)
	}
}

// The foundation analyzer must reject an omitted kind even with a default.
func TestFailureSwitchMustBeExhaustive(t *testing.T) {
	file := filepath.Join(t.TempDir(), "missing.go")
	source := `// SPDX-License-Identifier: Apache-2.0
package fixture
import "github.com/thisisjun786/lina/kit/provider"
func classify(k provider.FailureKind) {
 switch k {
 case provider.NeedsSignIn, provider.NotEligible, provider.UsageCapped, provider.Unavailable:
 default:
 }
}`
	if err := os.WriteFile(file, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.CommandContext(t.Context(), "go", "tool", "exhaustive", file).CombinedOutput()
	if err == nil || !strings.Contains(string(out), "NotAccepted") {
		t.Fatalf("missing case accepted: %v\n%s", err, out)
	}
}
