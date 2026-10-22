// SPDX-License-Identifier: Apache-2.0
package loop_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
)

func TestRuntimeToolBridgeUsesAdmissionAndJournal(t *testing.T) {
	var order []string
	cfg := config(func(ctx context.Context, request provider.Request) (provider.Stream, error) {
		if request.Bridge == nil {
			t.Fatal("runtime has no Core tool bridge")
		}
		result, err := request.Bridge.Call(ctx, call("runtime-call"))
		if err != nil {
			t.Fatal(err)
		}
		var saved loop.Result
		if err := json.Unmarshal(result.Output, &saved); err != nil || saved.Code != loop.OK {
			t.Fatalf("result=%s err=%v", result.Output, err)
		}
		return response(), nil
	}, func(_ context.Context, c provider.ToolCall) loop.Result {
		order = append(order, "execute")
		return success(c)
	})
	cfg.RuntimeTools = true
	cfg.Journal.Calls = func(context.Context, int, []provider.ToolCall) error { order = append(order, "calls"); return nil }
	cfg.Journal.Result = func(context.Context, int, int, loop.Result) error { order = append(order, "result"); return nil }
	out, err := loop.Run(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(order, []string{"calls", "execute", "result"}) || len(out.Results) != 1 {
		t.Fatalf("order=%v results=%v", order, out.Results)
	}
}
