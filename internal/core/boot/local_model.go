// SPDX-License-Identifier: Apache-2.0

package boot

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/localmodel"
	"github.com/thisisjun786/lina/internal/core/secrets"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/protocol"
)

func localModelOperation(r *conversationRuntime) map[protocol.Kind]clientsrv.Operation {
	return map[protocol.Kind]clientsrv.Operation{protocol.KindLocalTask: {Capability: protocol.Capability{Name: "local_model", Version: "1.0"}, Handle: r.localTask}}
}

func (r *conversationRuntime) localTask(ctx context.Context, s clientsrv.Session, e protocol.Envelope) (clientsrv.Result, error) {
	if err := r.authorize(s, nil); err != nil {
		return clientsrv.Result{}, err
	}
	request, ok := e.Payload.(protocol.LocalTask)
	if !ok || request.Text == "" {
		return clientsrv.Result{}, &clientsrv.OperationError{Code: protocol.ErrorMalformed}
	}
	// The same admission mask and model-bound projection as conversation input.
	// Both the helper and the main model see the projection, never the value.
	masked, err := secrets.Mask(request.Text)
	if err != nil {
		return clientsrv.Result{}, &clientsrv.OperationError{Code: protocol.ErrorMalformed}
	}
	task := localmodel.Task{Kind: request.Task, Text: masked.DerivedText()}
	if request.Preference != nil {
		task.Preference = *request.Preference
	}
	if request.Schema != nil {
		task.Schema = json.RawMessage(*request.Schema)
	}
	if request.Images != nil {
		n, err := request.Images.Int64()
		if err != nil || n < 0 || n > 8 {
			return clientsrv.Result{}, &clientsrv.OperationError{Code: protocol.ErrorMalformed}
		}
		task.Images = int(n)
	}
	if request.Tools != nil {
		task.Tools = append([]string{}, (*request.Tools)...)
	}
	if request.MacLocal != nil {
		task.MacLocal = *request.MacLocal
	}
	held, err := r.activities.Acquire(ctx, lease.Activity{Class: lease.Live, Reason: "on-device model"})
	if err != nil {
		return clientsrv.Result{}, err
	}
	defer held.Release()
	deps := localmodel.Deps{Profile: r.onDeviceProfile, Ledger: localmodel.CanonLedger{DB: r.db}}
	if r.onDevice != nil {
		deps.Local = localmodel.Apple{Adapter: r.onDevice}
	}
	if r.route != nil && r.route.Adapter != nil {
		deps.Main = localmodel.AdapterModel{Adapter: r.route.Adapter, Model: r.route.Model, Request: r.route.Request}
	}
	out, err := localmodel.Serve(ctx, held, task, deps)
	if errors.Is(err, localmodel.ErrMalformed) {
		return clientsrv.Result{}, &clientsrv.OperationError{Code: protocol.ErrorMalformed}
	}
	if err != nil {
		return clientsrv.Result{}, &clientsrv.OperationError{Code: protocol.ErrorUnavailable}
	}
	result := protocol.LocalTaskResult{Route: out.Row.Route, Reason: out.Row.Reason, Provider: string(out.Row.Provider), Model: out.Row.Model, ServedID: out.Row.ID}
	if out.Text != "" {
		result.Text = &out.Text
	}
	if len(out.Object) > 0 {
		object := string(out.Object)
		result.Object = &object
	}
	return clientsrv.Result{Kind: protocol.KindLocalTaskResult, Effect: protocol.EffectNone, Payload: result}, nil
}
