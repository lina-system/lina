// SPDX-License-Identifier: Apache-2.0
package clientsrv

import (
	"context"
	"github.com/thisisjun786/lina/kit/protocol"
)

var ReplayCapability = protocol.Capability{Name: "replay", Version: "1.0"}

type ReplayService interface {
	Export(context.Context, Session, protocol.ExportRequest) (protocol.ExportResult, error)
	Replay(context.Context, Session, protocol.ReplayRequest) (protocol.ReplayResult, error)
}

func ReplayOperations(service ReplayService) map[protocol.Kind]Operation {
	handle := func(ctx context.Context, s Session, e protocol.Envelope) (Result, error) {
		if service == nil {
			return Result{}, &OperationError{Code: protocol.ErrorUnavailable}
		}
		switch p := e.Payload.(type) {
		case protocol.ExportRequest:
			r, err := service.Export(ctx, s, p)
			return Result{Kind: protocol.KindExportResult, Payload: r}, err
		case protocol.ReplayRequest:
			r, err := service.Replay(ctx, s, p)
			return Result{Kind: protocol.KindReplayResult, Payload: r}, err
		}
		return Result{}, &OperationError{Code: protocol.ErrorMalformed}
	}
	return map[protocol.Kind]Operation{protocol.KindExportRequest: {Capability: ReplayCapability, Handle: handle}, protocol.KindReplayRequest: {Capability: ReplayCapability, Handle: handle}}
}
