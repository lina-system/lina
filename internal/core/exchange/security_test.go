// SPDX-License-Identifier: Apache-2.0
package exchange_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/exchange"
	"github.com/thisisjun786/lina/kit/secret"
)

func credential() string { return "ghp_" + strings.Repeat("Synthetic", 5) }
func TestSecretShapedReferencesNeverEnterMailbox(t *testing.T) {
	for _, field := range []string{"delivery", "from", "to", "check", "material", "revision"} {
		t.Run(field, func(t *testing.T) {
			s := exchange.New(database(t))
			req := request()
			token := credential()
			switch field {
			case "delivery":
				req.DeliveryID = token
			case "from":
				req.From = token
			case "to":
				req.To = token
			case "check":
				req.HandOff.Check = token
			case "material":
				req.HandOff.Materials[0].ID = token
			case "revision":
				req.HandOff.Materials[0].Revision = token
			}
			r, err := s.Start(t.Context(), req, now)
			if !errors.Is(err, exchange.ErrInvalid) || r.ID != "" {
				t.Fatalf("credential-shaped %s accepted: %+v %v", field, r, err)
			}
			lines, err := s.Log(t.Context())
			if err != nil || len(lines) != 0 {
				t.Fatalf("rejected request persisted: %+v %v", lines, err)
			}
		})
	}
}
func TestSecretShapedAnswerEvidenceIsRejectedBeforeCheck(t *testing.T) {
	for _, field := range []string{"event", "material", "revision"} {
		t.Run(field, func(t *testing.T) {
			s := exchange.New(database(t))
			r := start(t, s)
			evidence := exchange.Evidence{EventID: "event", Material: exchange.Material{ID: "material", Revision: "revision"}, At: now}
			switch field {
			case "event":
				evidence.EventID = credential()
			case "material":
				evidence.Material.ID = credential()
			case "revision":
				evidence.Material.Revision = credential()
			}
			checks := 0
			err := s.Answer(t.Context(), r.DeliveryID, "answerer", exchange.Answer{Text: "answer", Evidence: []exchange.Evidence{evidence}}, now, func(context.Context, string, string, exchange.Answer) (bool, error) { checks++; return true, nil })
			if !errors.Is(err, exchange.ErrInvalid) || checks != 0 || record(t, s, r.ID).Outcome != exchange.Pending {
				t.Fatalf("credential evidence %s accepted: checks=%d err=%v", field, checks, err)
			}
		})
	}
}

type secretKnowledge struct{ knowledge }

func (k *secretKnowledge) Persona(ctx context.Context, turn, agent string) (agents.Preparation, error) {
	p, err := k.knowledge.Persona(ctx, turn, agent)
	p.Layers.Core += " " + credential()
	return p, err
}
func (k *secretKnowledge) Common(ctx context.Context) ([]string, error) {
	items, err := k.knowledge.Common(ctx)
	return append(items, credential()), err
}
func (k *secretKnowledge) Material(ctx context.Context, m exchange.Material) (string, error) {
	text, err := k.knowledge.Material(ctx, m)
	return text + " " + credential() + " " + secret.Placeholder, err
}
func TestKnowledgeProjectionRemovesSecretsBeforeModel(t *testing.T) {
	s := exchange.New(database(t))
	r := start(t, s)
	p, err := s.Prepare(t.Context(), r.DeliveryID, "answerer", now, &secretKnowledge{knowledge{t: t}})
	if err != nil {
		t.Fatal(err)
	}
	raw := p.Request.Instructions + string(mustJSON(t, p.Request.Items))
	if strings.Contains(raw, credential()) || strings.Contains(raw, secret.Placeholder) {
		t.Fatal("secret reached model request")
	}
	if !strings.Contains(raw, "named-material") || !strings.Contains(raw, "persona-core") {
		t.Fatal("non-secret knowledge lost")
	}
}
