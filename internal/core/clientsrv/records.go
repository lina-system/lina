// SPDX-License-Identifier: Apache-2.0
package clientsrv

import (
	"context"
	"github.com/thisisjun786/lina/kit/protocol"
)

type RecordService interface {
	Record(context.Context, Session, protocol.RecordRequest) (protocol.RecordResult, error)
}

func RecordOperations(service RecordService) map[protocol.Kind]Operation {
	return map[protocol.Kind]Operation{protocol.KindRecordRequest: {Capability: protocol.Capability{Name: "records", Version: "1.0"}, Handle: func(ctx context.Context, s Session, e protocol.Envelope) (Result, error) {
		p, ok := e.Payload.(protocol.RecordRequest)
		if !ok {
			return Result{}, &OperationError{Code: protocol.ErrorMalformed}
		}
		result, err := service.Record(ctx, s, p)
		effect := protocol.EffectNone
		if err == nil && p.Operation == "save" && result.Code == nil {
			effect = protocol.EffectApplied
		}
		if result.Code != nil && *result.Code == "record_operation_unknown" {
			effect = protocol.EffectUnknown
		}
		return Result{Kind: protocol.KindRecordResult, Payload: result, Effect: effect}, err
	}}}
}
