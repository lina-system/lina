// SPDX-License-Identifier: Apache-2.0
package atropos_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/assemble"
	"github.com/thisisjun786/lina/internal/core/assemble/atropos"
)

func TestPacketDeadlineKeepsMandatoryContext(t *testing.T) {
	minimum := assemble.ContextPacket{{ID: "clock", Kind: assemble.Clock, Text: "now"}, {ID: "skill", Kind: assemble.SkillBody, Revision: "v1"}}
	finished := make(chan struct{})
	packet, report, err := atropos.Packet(t.Context(), minimum, time.Nanosecond, func(ctx context.Context) (assemble.ContextPacket, error) {
		defer close(finished)
		<-ctx.Done()
		return assemble.ContextPacket{{ID: "late", Kind: assemble.Memory}}, ctx.Err()
	})
	<-finished
	if err != nil || !report.TimedOut || !reflect.DeepEqual(packet, minimum) {
		t.Fatalf("%+v %+v %v", packet, report, err)
	}
	packet[0].Text = "changed"
	if minimum[0].Text != "now" {
		t.Fatal("packet aliases minimum")
	}
}

func TestPacketCapDoesNotWaitForCollector(t *testing.T) {
	release, finished := make(chan struct{}), make(chan struct{})
	_, report, err := atropos.Packet(t.Context(), nil, time.Nanosecond, func(context.Context) (assemble.ContextPacket, error) {
		defer close(finished)
		<-release
		return nil, nil
	})
	close(release)
	<-finished
	if err != nil || !report.TimedOut {
		t.Fatalf("%+v %v", report, err)
	}
}

func TestPacketCollectionAndParentCancellation(t *testing.T) {
	minimum := assemble.ContextPacket{{ID: "clock", Kind: assemble.Clock}}
	optional := assemble.ContextPacket{{ID: "memory", Kind: assemble.Memory, Evidence: []assemble.Ref{{ID: "e"}}}}
	packet, report, err := atropos.Packet(t.Context(), minimum, 0, func(ctx context.Context) (assemble.ContextPacket, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 250*time.Millisecond {
			t.Error("missing default cap")
		}
		return optional, nil
	})
	if err != nil || report.TimedOut || len(packet) != 2 {
		t.Fatalf("%+v %+v %v", packet, report, err)
	}
	packet[1].Evidence[0].ID = "changed"
	if optional[0].Evidence[0].ID != "e" {
		t.Fatal("packet aliases collector")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := atropos.Packet(ctx, minimum, 0, nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, _, err := atropos.Packet(t.Context(), optional, 0, nil); err == nil {
		t.Fatal("optional data accepted as mandatory")
	}
	failure := errors.New("read failed")
	if _, _, err := atropos.Packet(t.Context(), minimum, 0, func(context.Context) (assemble.ContextPacket, error) { return nil, failure }); !errors.Is(err, failure) {
		t.Fatal(err)
	}
}
