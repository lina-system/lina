// SPDX-License-Identifier: Apache-2.0
package replay

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/toolcatalog"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/proc"
	"github.com/thisisjun786/lina/kit/provider"
	"github.com/thisisjun786/lina/kit/sandbox"
)

// WorkerInput contains masked request data only. Credentials never enter the
// child. The same kit loop runs inside the OS sandbox, with model transport
// brokered by Core over inherited pipes and no network in the child.
type WorkerInput struct {
	Request   provider.Request
	Calls     []Call
	ReadNames []string
}
type workerFrame struct {
	Read    *provider.ToolCall `json:"read,omitempty"`
	Result  *loop.Result       `json:"result,omitempty"`
	Request *provider.Request  `json:"request,omitempty"`
	Event   *provider.Event    `json:"event,omitempty"`
	Meta    *provider.Meta     `json:"meta,omitempty"`
	Outcome *loop.Outcome      `json:"outcome,omitempty"`
	Error   string             `json:"error,omitempty"`
	End     bool               `json:"end,omitempty"`
}

const maxWorkerFrame = 8 << 20

func decoder(r io.Reader) *bufio.Scanner {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 4096), maxWorkerFrame)
	return s
}
func readFrame(s *bufio.Scanner, value any) error {
	if !s.Scan() {
		if err := s.Err(); err != nil {
			return err
		}
		return io.EOF
	}
	return json.Unmarshal(s.Bytes(), value)
}

type pipeStream struct {
	frames []provider.Event
	meta   provider.Meta
}

func (s *pipeStream) Next(context.Context) (provider.Event, error) {
	if len(s.frames) == 0 {
		return provider.Event{}, io.EOF
	}
	e := s.frames[0]
	s.frames = s.frames[1:]
	return e, nil
}
func (s *pipeStream) Meta() provider.Meta { return s.meta }
func (s *pipeStream) Close() error        { return nil }

// Worker is a private subprocess entry, not a second engine or a store. The
// broker refuses arbitrary requests and credentials even if input is malformed.
func Worker(ctx context.Context, input io.Reader, output io.Writer) error {
	scan := decoder(input)
	enc := json.NewEncoder(output)
	var initial WorkerInput
	if err := readFrame(scan, &initial); err != nil {
		return err
	}
	if initial.Request.Credential.Account != "" || initial.Request.Bridge != nil {
		return ErrInvalid
	}
	reads := map[string]loop.Tool{}
	for _, name := range initial.ReadNames {
		if !LocalRead(name) {
			return ErrInvalid
		}
		reads[name] = loop.Tool{Validate: func(provider.ToolCall) (string, error) { return "", nil }, Execute: func(_ context.Context, call provider.ToolCall) loop.Result {
			if err := enc.Encode(workerFrame{Read: &call}); err != nil {
				return loop.Result{ID: call.ID, Code: loop.Failed}
			}
			var frame workerFrame
			if err := readFrame(scan, &frame); err != nil || frame.Result == nil || frame.Result.ID != call.ID {
				return loop.Result{ID: call.ID, Code: loop.Failed}
			}
			return *frame.Result
		}}
	}
	out, err := Run(ctx, RunConfig{Request: initial.Request, Calls: initial.Calls, Reads: reads, Stream: func(_ context.Context, r provider.Request) (provider.Stream, error) {
		if err := enc.Encode(workerFrame{Request: &r}); err != nil {
			return nil, err
		}
		s := &pipeStream{}
		for {
			var f workerFrame
			if err := readFrame(scan, &f); err != nil {
				return nil, err
			}
			if f.Error != "" {
				return nil, errors.New("replay_model_failed")
			}
			if f.Event != nil {
				s.frames = append(s.frames, *f.Event)
			}
			if f.Meta != nil {
				s.meta = *f.Meta
			}
			if f.End {
				return s, nil
			}
		}
	}})
	frame := workerFrame{Outcome: &out}
	if errors.Is(err, ErrExcluded) {
		frame.Outcome = nil
		frame.Error = ErrExcluded.Error()
	} else if err != nil {
		frame.Outcome = nil
		frame.Error = "replay_failed"
	}
	return enc.Encode(frame)
}

type SandboxConfig struct {
	Executable  string
	Denied      []string
	Environment []string
	Stream      loop.StreamFunc
	Reads       map[string]loop.Tool
}

// Sandboxed owns and removes a fresh scratch directory and reaps its child on
// success, cancellation and broker failure. The conversation workspace is never
// a write root. No shell or candidate code is executed.
func Sandboxed(ctx context.Context, cfg SandboxConfig, initial WorkerInput) (out loop.Outcome, err error) {
	if cfg.Stream == nil {
		return out, ErrInvalid
	}
	all, err := recordedDeclarations(initial.Calls)
	if err != nil {
		return out, err
	}
	catalog := &toolcatalog.Catalog{First: initial.Request.Tools, All: all, Offset: len(initial.Request.Items)}
	scratch, err := os.MkdirTemp("", "lina-replay-")
	if err != nil {
		return out, err
	}
	defer os.RemoveAll(scratch)
	owner := proc.NewOwner()
	defer owner.Close(context.WithoutCancel(ctx))
	box := sandbox.New(owner, sandbox.Config{WorkDir: scratch, ReadDenied: cfg.Denied, Env: cfg.Environment})
	l := lease.New(func() {})
	defer l.Release()
	childIn, parentOut := io.Pipe()
	parentIn, childOut := io.Pipe()
	defer childIn.Close()
	defer parentOut.Close()
	defer parentIn.Close()
	defer childOut.Close()
	childCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		e := box.Run(childCtx, l, sandbox.Command{Path: cfg.Executable, Args: []string{"replay-worker"}, Stdin: childIn, Stdout: childOut, Stderr: io.Discard})
		childOut.CloseWithError(e)
		childIn.CloseWithError(e)
		done <- e
	}()
	defer func() {
		if err != nil {
			cancel()
		}
		parentOut.Close()
		parentIn.Close()
		err = errors.Join(err, <-done)
	}()
	initial.ReadNames = nil
	for name := range cfg.Reads {
		if !LocalRead(name) {
			return out, ErrInvalid
		}
		initial.ReadNames = append(initial.ReadNames, name)
	}
	initial.Request.Credential = provider.Credential{}
	initial.Request.Bridge = nil
	enc := json.NewEncoder(parentOut)
	if err = enc.Encode(initial); err != nil {
		return out, err
	}
	scan := decoder(parentIn)
	for {
		var frame workerFrame
		if err = readFrame(scan, &frame); err != nil {
			return out, err
		}
		if frame.Error != "" {
			if frame.Error == ErrExcluded.Error() {
				return out, ErrExcluded
			}
			return out, errors.New("replay_failed")
		}
		if frame.Outcome != nil {
			return *frame.Outcome, nil
		}
		if frame.Read != nil {
			call := *frame.Read
			tool, ok := cfg.Reads[call.Name]
			if !ok || tool.Validate == nil || tool.Execute == nil {
				return out, ErrInvalid
			}
			if target, e := tool.Validate(call); e != nil || target != "" {
				return out, ErrInvalid
			}
			result := tool.Execute(ctx, call)
			result.ID = call.ID
			if err = enc.Encode(workerFrame{Result: &result}); err != nil {
				return out, err
			}
			continue
		}
		if frame.Request == nil {
			return out, ErrInvalid
		}
		request := *frame.Request
		// The child owns only loop evolution. It cannot change the account, model,
		// instructions, or offered tools selected by the authenticated operation.
		// Restore loaded schemas only from that operation's recorded catalog.
		request.Model = initial.Request.Model
		request.Effort = initial.Request.Effort
		request.Instructions = initial.Request.Instructions
		request.Tools = initial.Request.Tools
		request.Fields = initial.Request.Fields
		request.Purpose = provider.Replay
		request, err = catalog.Transform(ctx, request)
		if err != nil {
			return out, err
		}
		stream, e := cfg.Stream(ctx, request)
		if e != nil {
			return out, e
		}
		for {
			event, e := stream.Next(ctx)
			if e == io.EOF {
				meta := stream.Meta()
				err = enc.Encode(workerFrame{Meta: &meta, End: true})
				break
			}
			if e != nil {
				err = e
				break
			}
			if err = enc.Encode(workerFrame{Event: &event}); err != nil {
				break
			}
		}
		closeErr := stream.Close()
		if err = errors.Join(err, closeErr); err != nil {
			return out, err
		}
	}
}

// InstructionBuilder is the existing pure assembly function supplied by boot.
type InstructionBuilder func(string, agents.Preparation, []provider.ToolDecl) (string, error)

// Request applies candidate text to pinned layers and the recorded identity.
func Request(ep Episode, candidates map[string]string, model, effort string, build InstructionBuilder) (provider.Request, error) {
	p := ep.Persona
	for name, text := range candidates {
		switch filepath.ToSlash(name) {
		case "core.md":
			p.Core = text
		case "default-voice.md":
			p.DefaultVoice = text
		case "user-voice.md":
			p.UserVoice = text
		case "growth-snapshot.md":
			p.GrowthSnapshot = text
		default:
			return provider.Request{}, ErrInvalid
		}
	}
	instructions := strings.Join([]string{p.Core, p.DefaultVoice, p.UserVoice, p.GrowthSnapshot}, "\n")
	switch ep.Evidence.Assembly.InstructionFormat {
	case "": // Exact legacy layer assembly, still checked against its digest.
	case "agent-v1", "persona-v1", "persona-tui-v1", "persona-tui-work-method-v1":
		a := ep.Evidence.Assembly
		if build == nil || a.AgentName == "" || a.OwnerPersonID == "" {
			return provider.Request{}, Exclusion("missing_assembly")
		}
		var err error
		instructions, err = build(a.InstructionFormat, agents.Preparation{AgentID: a.AgentID, Name: a.AgentName, OwnerPersonID: a.OwnerPersonID, Layers: p, WorkMethod: ep.WorkMethod}, ep.Evidence.Tools)
		if err != nil {
			return provider.Request{}, err
		}
	default:
		return provider.Request{}, Exclusion("missing_assembly")
	}
	r := provider.Request{Model: ep.Evidence.Model, Effort: ep.Evidence.Effort, Instructions: instructions, Items: ep.Items, Tools: ep.Evidence.Tools, Fields: ep.Evidence.Fields, Purpose: provider.Replay}
	if model != "" {
		r.Model = provider.ModelRef(model)
	}
	if effort != "" {
		r.Effort = provider.Effort(effort)
	}
	return r, nil
}
