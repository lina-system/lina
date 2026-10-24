// SPDX-License-Identifier: Apache-2.0

// Package applefm is the on-device Apple Foundation Models adapter.
// It speaks one JSON line to a helper and never reads the secret store.
package applefm

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"path/filepath"
	"time"

	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/proc"
	"github.com/thisisjun786/lina/kit/provider"
)

const (
	ProviderID        provider.ProviderID = "apple"
	OnDeviceModel                         = "apple-foundation-on-device"
	PrivateCloudModel                     = "apple-foundation-private-cloud"
)

// Profile is the helper's capability row for one model.
type Profile struct {
	ContextTokens    int    `json:"context_tokens"`
	ToolCalling      bool   `json:"tool_calling"`
	GuidedGeneration bool   `json:"guided_generation"`
	ImageInput       bool   `json:"image_input"`
	Model            string `json:"model"`
	PrivateCloud     bool   `json:"private_cloud"`
}

// Settings selects the helper. An empty Helper means the route is off.
type Settings struct {
	Helper       string
	Model        string
	PrivateCloud bool
	Env          []string
	Args         []string
}

type Request struct {
	Instructions string
	Input        string
	Schema       json.RawMessage
}

type Result struct {
	Text   string
	Object json.RawMessage
	Usage  provider.Usage
}

// Adapter runs the helper once per call. It does not keep a session.
type Adapter struct {
	settings Settings
}

func New(settings Settings) (*Adapter, error) {
	if !filepath.IsAbs(settings.Helper) || settings.Model == "" {
		return nil, &provider.Failure{Kind: provider.NotAccepted}
	}
	settings.Env = append([]string{}, settings.Env...)
	settings.Args = append([]string{}, settings.Args...)
	return &Adapter{settings: settings}, nil
}

func (*Adapter) Provider() provider.ProviderID { return ProviderID }
func (*Adapter) Class() provider.Class         { return provider.ModelAPI }
func (*Adapter) Capabilities(provider.ModelRef) provider.CapabilitySet {
	return nil
}
func (a *Adapter) Models(context.Context, lease.Lease, provider.Credential) (provider.ModelList, error) {
	return provider.ModelList{Models: []provider.ModelRef{provider.ModelRef(a.settings.Model)}, Observed: time.Now().UTC()}, nil
}
func (*Adapter) Quota(context.Context, provider.Credential) ([]provider.QuotaRow, error) {
	return nil, nil
}

func (a *Adapter) ModelName() string { return a.settings.Model }

func (a *Adapter) Probe(ctx context.Context, l lease.Lease) (Profile, error) {
	id, err := newID()
	if err != nil {
		return Profile{}, err
	}
	resp, err := a.call(ctx, l, map[string]any{"id": id, "op": "profile", "model": a.settings.Model})
	if err != nil {
		return Profile{}, err
	}
	if resp.Profile == nil || resp.Profile.Model != a.settings.Model || resp.Profile.ContextTokens <= 0 || resp.Profile.PrivateCloud != a.settings.PrivateCloud {
		return Profile{}, &provider.Failure{Kind: provider.Unavailable}
	}
	return *resp.Profile, nil
}

func (a *Adapter) Complete(ctx context.Context, l lease.Lease, req Request) (Result, error) {
	if req.Input == "" || len(req.Schema) > 0 && !ValidSchema(req.Schema) {
		return Result{}, &provider.Failure{Kind: provider.NotAccepted}
	}
	id, err := newID()
	if err != nil {
		return Result{}, err
	}
	body := map[string]any{"id": id, "op": "complete", "model": a.settings.Model, "instructions": req.Instructions, "input": req.Input}
	if len(req.Schema) > 0 {
		body["schema"] = json.RawMessage(req.Schema)
	}
	resp, err := a.call(ctx, l, body)
	if err != nil {
		return Result{}, err
	}
	if len(req.Schema) == 0 {
		return Result{Text: resp.Text, Usage: resp.usage()}, nil
	}
	if resp.SchemaOK == nil || !*resp.SchemaOK {
		return Result{}, ErrSchemaMiss
	}
	object, err := ValidateObject(req.Schema, resp.Object)
	if err != nil {
		return Result{}, ErrSchemaMiss
	}
	return Result{Object: object, Usage: resp.usage()}, nil
}

func (a *Adapter) Stream(ctx context.Context, l lease.Lease, req provider.Request) (provider.Stream, error) {
	if err := provider.ValidateUse(req, a.Capabilities(req.Model), provider.Admitted("apple")); err != nil {
		return nil, err
	}
	if string(req.Model) != a.settings.Model {
		return nil, &provider.Failure{Kind: provider.NotAccepted}
	}
	var input string
	if len(req.Items) == 1 {
		var item struct {
			Content string `json:"content"`
		}
		if json.Unmarshal(req.Items[0], &item) != nil {
			return nil, &provider.Failure{Kind: provider.NotAccepted}
		}
		input = item.Content
	}
	result, err := a.Complete(ctx, l, Request{Instructions: req.Instructions, Input: input})
	if err != nil {
		return nil, err
	}
	return &textStream{text: result.Text, meta: provider.Meta{Class: provider.ModelAPI, Usage: result.Usage}}, nil
}

type helperResponse struct {
	ID          string          `json:"id"`
	OK          bool            `json:"ok"`
	Text        string          `json:"text"`
	Object      json.RawMessage `json:"object"`
	SchemaOK    *bool           `json:"schema_ok"`
	Unavailable bool            `json:"unavailable"`
	Profile     *Profile        `json:"profile"`
	Usage       struct {
		Input  *int64 `json:"input"`
		Output *int64 `json:"output"`
	} `json:"usage"`
}

func (r helperResponse) usage() provider.Usage {
	return provider.Usage{Input: r.Usage.Input, Output: r.Usage.Output}
}

func (a *Adapter) call(ctx context.Context, l lease.Lease, request any) (helperResponse, error) {
	body, err := json.Marshal(request)
	if err != nil {
		return helperResponse{}, &provider.Failure{Kind: provider.NotAccepted}
	}
	var header struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(body, &header) != nil || header.ID == "" {
		return helperResponse{}, &provider.Failure{Kind: provider.NotAccepted}
	}
	body = append(body, '\n')
	stdoutR, stdoutW := io.Pipe()
	errCh := make(chan error, 1)
	go func() {
		owner := proc.NewOwner()
		err := owner.Run(ctx, l, proc.Command{Path: a.settings.Helper, Args: a.settings.Args, Env: a.settings.Env, Stdin: bytes.NewReader(body), Stdout: stdoutW, Stderr: io.Discard})
		_ = stdoutW.CloseWithError(err)
		errCh <- err
	}()
	scanner := bufio.NewScanner(stdoutR)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	var raw []byte
	if scanner.Scan() {
		raw = append([]byte(nil), scanner.Bytes()...)
	}
	scanErr := scanner.Err()
	_ = stdoutR.Close()
	<-errCh
	if scanErr != nil || len(raw) == 0 {
		return helperResponse{}, &provider.Failure{Kind: provider.Unavailable}
	}
	var resp helperResponse
	if json.Unmarshal(raw, &resp) != nil || resp.ID != header.ID || resp.Unavailable {
		return helperResponse{}, &provider.Failure{Kind: provider.Unavailable}
	}
	if resp.SchemaOK != nil && !*resp.SchemaOK {
		resp.Object = nil
		resp.Text = ""
		return resp, nil
	}
	if !resp.OK {
		return helperResponse{}, &provider.Failure{Kind: provider.Unavailable}
	}
	return resp, nil
}

func newID() (string, error) {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf[:]), nil
}

type textStream struct {
	text string
	meta provider.Meta
	done bool
}

func (s *textStream) Next(context.Context) (provider.Event, error) {
	if s.done {
		return provider.Event{}, io.EOF
	}
	s.done = true
	return provider.Event{Kind: provider.Delta, Delta: s.text}, nil
}
func (s *textStream) Meta() provider.Meta { return s.meta }
func (*textStream) Close() error          { return nil }
