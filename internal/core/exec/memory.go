// SPDX-License-Identifier: Apache-2.0

// Package exec composes Core's memory write and recall tool operations. It owns
// the commit boundary: an item returned inside a transaction is not a receipt.
package exec

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/memory"
	"github.com/thisisjun786/lina/kit/provider"
)

var (
	ErrBackgroundRequired = errors.New("recall_person_requires_background")
	ErrAuxUnavailable     = errors.New("recall_person_auxiliary_unavailable")
)

const RecallItemLimit = 12
const RecallTimeout = 10 * time.Second

type Memory struct {
	db      *canon.Store
	store   *memory.Store
	events  *ledger.Store
	write   func(context.Context, func(*canon.Tx) error) error
	resolve memory.ResolveEvidence
	prepare func(context.Context, memory.Write) (memory.ResolveEvidence, error)
	search  RecallSearch
	aux     Auxiliary
}

// newMemory is bound by the trusted Core flow. Resolve and ledger must read
// canonical sources, and auxiliary must use the configured model/budget path,
// honor cancellation and make exactly one request without tools or retries.
func newMemory(db *canon.Store, resolve memory.ResolveEvidence, search RecallSearch, aux Auxiliary) *Memory {
	m := &Memory{db: db, store: memory.New(db), resolve: resolve, search: search, aux: aux}
	if db != nil {
		m.write = db.Write
		m.events = ledger.New(db, nil)
		if m.search == nil {
			m.search = m.recallSources
		}
	}
	return m
}

// RememberReceipt has no success text before canon acknowledges the commit.
// Its private bit prevents constructing a success receipt from a draft item.
// Tool adapters should expose this receipt, not synthesize an acknowledgement.
type RememberReceipt struct {
	ID        string
	Revision  int64
	committed bool
}

func (r RememberReceipt) Remembered() bool { return r.committed }
func (m *Memory) Remember(ctx context.Context, in memory.Write) (RememberReceipt, error) {
	if m == nil || m.write == nil {
		return RememberReceipt{}, memory.ErrInvalid
	}
	resolve := m.resolve
	if m.prepare != nil {
		var err error
		resolve, err = m.prepare(ctx, in)
		if err != nil {
			return RememberReceipt{}, err
		}
	}
	var item memory.Item
	err := m.write(ctx, func(tx *canon.Tx) error { var err error; item, err = memory.Insert(ctx, tx, in, resolve); return err })
	if err != nil {
		return RememberReceipt{}, err
	}
	return RememberReceipt{ID: item.ID, Revision: item.Revision, committed: true}, nil
}

type Citation struct {
	ID       string
	Revision int64
}
type RecallSource struct {
	Citation
	Text     string
	Holder   memory.Holder
	Evidence []memory.Evidence
	Older    bool
}
type PersonRecall struct {
	Question, Query string
	Depth           int
	At              int64
	History         bool
}
type PersonAnswer struct {
	Sources []RecallSource
	Answer  []Sentence
}

// Each sentence carries citations separately; opaque free text is not parsed
// for purported references. A renderer must keep these citations with the text.
type Sentence struct {
	Text      string
	Citations []Citation
}
type RecallRequest struct {
	Kind     provider.RunKind
	Question string
	Sources  []RecallSource
}
type Auxiliary func(context.Context, RecallRequest) ([]Sentence, error)

// RecallSearch resolves a person query through the one recall entry. Its
// sources are ranked together and already carry one result per lineage.
type RecallSearch func(context.Context, memory.RecallScope, PersonRecall) ([]RecallSource, error)

func (m *Memory) RecallPerson(ctx context.Context, scope memory.RecallScope, in PersonRecall) (PersonAnswer, error) {
	if m == nil || strings.TrimSpace(in.Question) == "" || in.Depth < 0 {
		return PersonAnswer{}, memory.ErrInvalid
	}
	if in.Depth > 1 {
		return PersonAnswer{}, ErrBackgroundRequired
	}
	if strings.TrimSpace(in.Query) == "" {
		return PersonAnswer{}, memory.ErrInvalid
	}
	if in.Depth == 1 {
		scope.ExternalModel = true
	}
	result := PersonAnswer{}
	if m.search != nil {
		sources, err := m.search(ctx, scope, in)
		if err != nil {
			return PersonAnswer{}, err
		}
		result.Sources = sources
	}
	if err := ctx.Err(); err != nil {
		return PersonAnswer{}, err
	}
	if in.Depth == 0 || len(result.Sources) == 0 {
		return result, nil
	}
	if m.aux == nil {
		return result, ErrAuxUnavailable
	}
	capped, cancel := context.WithTimeout(ctx, RecallTimeout)
	defer cancel()
	// Retain the allowed revision set before calling external reasoning; mutation
	// of its input cannot widen the references accepted from its output.
	allowed := make(map[Citation]bool, len(result.Sources))
	input := make([]RecallSource, len(result.Sources))
	for i, source := range result.Sources {
		allowed[source.Citation] = true
		input[i] = source
		input[i].Evidence = slices.Clone(source.Evidence)
	}
	sentences, err := m.aux(capped, RecallRequest{Kind: provider.Auxiliary, Question: in.Question, Sources: input})
	if capped.Err() != nil {
		return result, capped.Err()
	}
	if err != nil {
		return result, err
	}
	for _, sentence := range sentences {
		citations := []Citation{}
		for _, citation := range sentence.Citations {
			if allowed[citation] && !slices.Contains(citations, citation) {
				citations = append(citations, citation)
			}
		}
		if strings.TrimSpace(sentence.Text) != "" && len(citations) > 0 {
			result.Answer = append(result.Answer, Sentence{Text: sentence.Text, Citations: citations})
		}
	}
	return result, nil
}
