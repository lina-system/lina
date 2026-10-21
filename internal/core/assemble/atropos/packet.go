// SPDX-License-Identifier: Apache-2.0

// Package atropos bounds per-turn context collection. Mandatory, authorized
// state is supplied before collection; background work is never awaited.
package atropos

import (
	"context"
	"errors"
	"time"

	"github.com/thisisjun786/lina/internal/core/assemble"
)

const DefaultCap = 250 * time.Millisecond

var ErrPacket = errors.New("invalid_context_packet")

// Collect reads only optional authorized context. It must honor cancellation
// and own cleanup of its reads. Late results are discarded, never persisted.
type Collect func(context.Context) (assemble.ContextPacket, error)
type Report struct{ TimedOut bool }

func snapshot(in assemble.ContextPacket) assemble.ContextPacket {
	out := append(assemble.ContextPacket(nil), in...)
	for i := range out {
		out[i].Evidence = append([]assemble.Ref(nil), in[i].Evidence...)
		out[i].Supersedes = append([]string(nil), in[i].Supersedes...)
	}
	return out
}

func Packet(ctx context.Context, minimum assemble.ContextPacket, cap time.Duration, collect Collect) (assemble.ContextPacket, Report, error) {
	if err := ctx.Err(); err != nil {
		return nil, Report{}, err
	}
	for _, item := range minimum {
		if item.Kind < assemble.Clock || item.Kind > assemble.SkillBody {
			return nil, Report{}, ErrPacket
		}
	}
	if cap < 0 {
		return nil, Report{}, ErrPacket
	}
	if cap == 0 {
		cap = DefaultCap
	}
	packet := snapshot(minimum)
	if collect == nil {
		return packet, Report{}, nil
	}
	bounded, cancel := context.WithTimeout(ctx, cap)
	defer cancel()
	type result struct {
		packet assemble.ContextPacket
		err    error
	}
	ready := make(chan result, 1)
	go func() { items, err := collect(bounded); ready <- result{snapshot(items), err} }()
	var r result
	select {
	case <-bounded.Done():
	case r = <-ready:
	}
	if err := ctx.Err(); err != nil {
		return nil, Report{}, err
	}
	if bounded.Err() != nil {
		return packet, Report{TimedOut: true}, nil
	}
	if r.err != nil {
		return nil, Report{}, r.err
	}
	for _, item := range r.packet {
		if item.Kind < assemble.Profile || item.Kind > assemble.External {
			return nil, Report{}, ErrPacket
		}
	}
	return append(packet, r.packet...), Report{}, nil
}
