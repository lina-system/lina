// SPDX-License-Identifier: Apache-2.0

package localmodel

import (
	"context"

	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/provider"
	"github.com/thisisjun786/lina/kit/provider/applefm"
)

// Apple adapts the helper to the short-task model boundary.
type Apple struct{ Adapter *applefm.Adapter }

func (a Apple) Identity() (provider.ProviderID, provider.ModelRef) {
	if a.Adapter == nil {
		return applefm.ProviderID, ""
	}
	return applefm.ProviderID, provider.ModelRef(a.Adapter.ModelName())
}

func (a Apple) Complete(ctx context.Context, l lease.Lease, call Call) (Completion, error) {
	if a.Adapter == nil {
		return Completion{}, ErrUnavailable
	}
	result, err := a.Adapter.Complete(ctx, l, applefm.Request{Instructions: call.Instructions, Input: call.Input, Schema: call.Schema})
	if err != nil {
		return Completion{}, err
	}
	return Completion{Text: result.Text, Object: result.Object, Usage: result.Usage}, nil
}
