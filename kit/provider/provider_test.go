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

func TestQuotaIdentityJSONCompatibility(t *testing.T) {
	const oldJSON = `{"Provider":"chatgpt","Account":"account","Window":"unknown","UsedPct":100,"ResetsAt":null,"State":"blocked","Source":"endpoint","Observed":"2026-10-01T00:00:00Z"}`
	var row QuotaRow
	if err := json.Unmarshal([]byte(oldJSON), &row); err != nil || row.Bucket != "" || row.LimitID != "" || row.State != QuotaBlocked {
		t.Fatalf("legacy quota changed: %+v %v", row, err)
	}
	raw, err := json.Marshal(row)
	if err != nil || strings.Contains(string(raw), "Bucket") || strings.Contains(string(raw), "LimitID") {
		t.Fatalf("legacy quota gained an identity: %s %v", raw, err)
	}
	row.LimitID, row.Bucket = "codex", "primary"
	raw, err = json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	var restored QuotaRow
	if err := json.Unmarshal(raw, &restored); err != nil || restored.LimitID != row.LimitID || restored.Bucket != row.Bucket {
		t.Fatalf("identity not durable: %+v %v", restored, err)
	}
	// The previous reader has no identity fields and must still read the block.
	var legacy struct {
		Provider ProviderID
		Account  AccountID
		Window   Window
		UsedPct  *float64
		State    QuotaState
	}
	if err := json.Unmarshal(raw, &legacy); err != nil || legacy.Provider != ChatGPT || legacy.Account != "account" || legacy.Window != UnknownWindow || legacy.State != QuotaBlocked || legacy.UsedPct == nil || *legacy.UsedPct != 100 {
		t.Fatalf("legacy reader lost quota evidence: %+v %v", legacy, err)
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
