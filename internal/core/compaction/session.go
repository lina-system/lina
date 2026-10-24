// SPDX-License-Identifier: Apache-2.0
package compaction

import (
	"bytes"
	"context"

	"github.com/thisisjun786/lina/kit/provider"
)

// ResolveItems binds newly committed loop items to their ledger ids. It must
// preserve item bytes and order; a missing/mismatched record stops dispatch.
type ResolveItems func(context.Context, []provider.Item) ([]Record, error)

// Session is owned by one serial conversation loop. The loop retains its raw
// outcome; only this request projection changes when compaction succeeds.
type Session struct {
	pass     Pass
	view     View
	original []provider.Item
	resolve  ResolveItems
	pending  bool
}

func NewSession(p Pass, v View, resolve ResolveItems) (*Session, error) {
	var err error
	p, err = p.setup(v)
	if err != nil {
		return nil, err
	}
	// The boundary already decided whether to compact early. Follow-up requests
	// inside the turn keep the ordinary trigger.
	p.Cold = false
	return &Session{pass: p, view: copyView(v), original: v.request().Items, resolve: resolve}, nil
}
func (s *Session) View() View { return copyView(s.view) }

// Sizes measures the current projection without another fitting or model pass.
func (s *Session) Sizes() (Sizes, error) { return s.pass.measure(s.view) }

func (s *Session) Transform(ctx context.Context, r provider.Request) (provider.Request, error) {
	if s.pending {
		return provider.Request{}, ErrContinuation
	}
	if len(r.Items) < len(s.original) {
		return provider.Request{}, ErrPass
	}
	for i, item := range s.original {
		if !bytes.Equal(item, r.Items[i]) {
			return provider.Request{}, ErrPass
		}
	}
	if len(r.Items) > len(s.original) {
		if s.resolve == nil {
			return provider.Request{}, ErrPass
		}
		items := copyItems(r.Items[len(s.original):])
		records, err := s.resolve(ctx, items)
		if err != nil {
			return provider.Request{}, err
		}
		if len(records) != len(items) {
			return provider.Request{}, ErrPass
		}
		for i, record := range records {
			if !bytes.Equal(record.Item, items[i]) {
				return provider.Request{}, ErrPass
			}
		}
		s.view.Active = append(s.view.Active, copyRecords(records)...)
		s.original = copyItems(r.Items)
	}
	s.view.Request = r
	out, err := s.pass.Fit(ctx, s.view, true)
	s.view = copyView(out.View)
	if err == ErrContinuation {
		s.pending = true
	}
	return out.Request, err
}
