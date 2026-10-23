// SPDX-License-Identifier: Apache-2.0
package messages

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/provider"
)

// BatchWindow is the deferral window a background job must exceed for a batch
// submission to be worth its waiting time.
const BatchWindow = 24 * time.Hour

// batchPollInterval is the first wait between status reads. The first read is
// immediate; later waits double up to the cap.
const (
	batchPollInterval = 5 * time.Second
	batchPollMax      = time.Minute
)

const batchCustomID = "lina"

type batchOutcome struct {
	events    []provider.Event
	usage     provider.Usage
	id        string
	requestID string
	served    bool
}

// batchApplies admits a batch only for a background job whose deadline lies
// beyond the window. Core's admission set must also carry the capability.
func (a *Adapter) batchApplies(ctx context.Context, r provider.Request) bool {
	if r.Purpose != provider.BackgroundJob {
		return false
	}
	if !a.Capabilities(r.Model).Has(provider.BatchCapability) || !a.AdmittedCapabilities(r.Model).Has(provider.BatchCapability) {
		return false
	}
	deadline, ok := ctx.Deadline()
	return ok && time.Until(deadline) > BatchWindow
}

// batch submits one deferred request and reads its result. A refused or failed
// submission, a canceled batch or an expired one reports served=false so the
// caller runs exactly one normal request. An authentication refusal is returned
// instead: a second request cannot fix it.
func (a *Adapter) batch(ctx context.Context, l lease.Lease, r provider.Request) (batchOutcome, error) {
	var out batchOutcome
	params, err := a.params(r)
	if err != nil {
		return out, err
	}
	payload, err := json.Marshal(struct {
		Requests []struct {
			CustomID string     `json:"custom_id"`
			Params   wireParams `json:"params"`
		} `json:"requests"`
	}{Requests: []struct {
		CustomID string     `json:"custom_id"`
		Params   wireParams `json:"params"`
	}{{batchCustomID, params}}})
	if err != nil {
		return out, err
	}
	window := time.Now().Add(BatchWindow)
	err = lease.Do(ctx, l, func(ctx context.Context) error {
		raw, err := a.call(ctx, r.Credential, http.MethodPost, "messages/batches", payload)
		if err != nil {
			return err
		}
		var submitted struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(raw, &submitted) != nil || submitted.ID == "" {
			return nil
		}
		out.id = submitted.ID
		wait := batchPollInterval
		for {
			status, err := a.batchStatus(ctx, r.Credential, out.id)
			if err != nil {
				var failure *provider.Failure
				if errors.As(err, &failure) && failure.Kind == provider.NeedsSignIn {
					return err
				}
				return nil
			}
			switch status {
			case "ended":
				events, usage, err := a.batchResult(ctx, r.Credential, out.id)
				if err != nil {
					return nil
				}
				out.events, out.usage, out.served = events, usage, true
				return nil
			case "in_progress":
			default:
				// canceling, canceled or an unknown state: run normally.
				return nil
			}
			if time.Now().Add(wait).After(window) {
				return nil
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
			}
			wait = min(wait*2, batchPollMax)
		}
	})
	return out, err
}

func (a *Adapter) batchStatus(ctx context.Context, c provider.Credential, id string) (string, error) {
	raw, err := a.call(ctx, c, http.MethodGet, "messages/batches/"+id, nil)
	if err != nil {
		return "", err
	}
	var status struct {
		ProcessingStatus string `json:"processing_status"`
	}
	if json.Unmarshal(raw, &status) != nil {
		return "", errInvalid
	}
	return status.ProcessingStatus, nil
}

func (a *Adapter) batchResult(ctx context.Context, c provider.Credential, id string) ([]provider.Event, provider.Usage, error) {
	response, err := a.request(ctx, c, http.MethodGet, "messages/batches/"+id+"/results", nil)
	if err != nil {
		return nil, provider.Usage{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, provider.Usage{}, responseFailure(response)
	}
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 4096), maxEventBytes)
	for scanner.Scan() {
		var line struct {
			CustomID string `json:"custom_id"`
			Result   struct {
				Type    string      `json:"type"`
				Message wireMessage `json:"message"`
			} `json:"result"`
		}
		if json.Unmarshal(scanner.Bytes(), &line) != nil {
			return nil, provider.Usage{}, errInvalid
		}
		if line.CustomID != batchCustomID || line.Result.Type != "succeeded" {
			continue
		}
		events, usage, err := batchEvents(line.Result.Message)
		return events, usage, err
	}
	if err := scanner.Err(); err != nil {
		return nil, provider.Usage{}, err
	}
	// A batch that ended without a usable result runs once as a normal request.
	return nil, provider.Usage{}, errInvalid
}

// batchEvents presents a completed batch message as the same item, call and
// terminal sequence a stream produces.
func batchEvents(m wireMessage) ([]provider.Event, provider.Usage, error) {
	if m.Role != "assistant" {
		return nil, provider.Usage{}, errInvalid
	}
	var usage provider.Usage
	mergeUsage(&usage, m.Usage)
	var events, pending []provider.Event
	for i, b := range m.Content {
		state := &blockState{kind: b.Type, id: b.ID, name: b.Name, input: b.Input, data: b.Data, whole: true}
		switch b.Type {
		case "text":
			state.text.WriteString(b.Text)
		case "thinking":
			state.thinking.WriteString(b.Thinking)
			state.signature.WriteString(b.Signature)
		case "redacted_thinking", "tool_use":
		default:
			return nil, usage, errInvalid
		}
		index := int64(i)
		item, call, err := finalize(index, state)
		if err == errSkipBlock {
			continue
		}
		if err != nil {
			return nil, usage, err
		}
		events = append(events, item)
		if call != nil {
			pending = append(pending, provider.Event{Kind: provider.ToolCallReady, Index: index, Call: call})
		}
	}
	events = append(events, pending...)
	events = append(events, provider.Event{Kind: provider.Terminal})
	return events, usage, nil
}

// call performs one bounded JSON request for the batch endpoints. It never
// retries, and a response body over the event bound fails the call.
func (a *Adapter) call(ctx context.Context, c provider.Credential, method, path string, body []byte) ([]byte, error) {
	response, err := a.request(ctx, c, method, path, body)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, responseFailure(response)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxEventBytes))
	if err != nil {
		return nil, classifyError(ctx, err, response.Header.Get("request-id"))
	}
	return raw, nil
}
