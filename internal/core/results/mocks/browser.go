// SPDX-License-Identifier: Apache-2.0
package mocks

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	generatedui "github.com/thisisjun786/lina/design"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/proc"
	"github.com/thisisjun786/lina/kit/result"
)

var ErrRenderer = errors.New("mock_renderer_unavailable")

type Chromium struct {
	Executable string
	Leaser     lease.Leaser
}
type Rendered struct {
	Checks []result.MockCheck
	PNG    []byte
	Page   string
}

type devtoolsLog struct {
	mu    sync.Mutex
	tail  string
	ready chan string
	sent  bool
}

func (w *devtoolsLog) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.tail += string(p)
	if len(w.tail) > 8192 {
		w.tail = w.tail[len(w.tail)-8192:]
	}
	for _, line := range strings.Split(w.tail, "\n") {
		if _, after, ok := strings.Cut(line, "DevTools listening on "); ok && !w.sent && strings.HasPrefix(after, "ws://127.0.0.1:") {
			w.ready <- strings.TrimSpace(after)
			w.sent = true
		}
	}
	return len(p), nil
}

type browser struct {
	conn         *websocket.Conn
	id           int
	session      string
	frameSession string
	exceptions   int
}

func (b *browser) call(ctx context.Context, method string, params any, out any) error {
	b.id++
	request := map[string]any{"id": b.id, "method": method, "params": params}
	if b.session != "" {
		request["sessionId"] = b.session
	}
	raw, _ := json.Marshal(request)
	if err := b.conn.Write(ctx, websocket.MessageText, raw); err != nil {
		return err
	}
	for {
		_, raw, err := b.conn.Read(ctx)
		if err != nil {
			return err
		}
		var reply struct {
			ID     int
			Method string
			Result json.RawMessage
			Error  *struct{ Message string }
		}
		if err = json.Unmarshal(raw, &reply); err != nil {
			return err
		}
		if reply.Method == "Runtime.exceptionThrown" {
			b.exceptions++
		}
		if reply.ID != b.id {
			continue
		}
		if reply.Error != nil {
			return fmt.Errorf("browser: %s", reply.Error.Message)
		}
		if out != nil {
			return json.Unmarshal(reply.Result, out)
		}
		return nil
	}
}
func (b *browser) evaluate(ctx context.Context, world int, expression string, out any) error {
	oldSession := b.session
	if world != 0 && b.frameSession != "" {
		b.session = b.frameSession
	}
	defer func() { b.session = oldSession }()
	params := map[string]any{"expression": expression, "returnByValue": true, "awaitPromise": true}
	if world > 0 {
		params["contextId"] = world
	}
	var r struct {
		Result           struct{ Value json.RawMessage }
		ExceptionDetails json.RawMessage
	}
	if err := b.call(ctx, "Runtime.evaluate", params, &r); err != nil {
		return err
	}
	if len(r.ExceptionDetails) > 0 {
		return errors.New("mock_page_error")
	}
	if out != nil {
		return json.Unmarshal(r.Result.Value, out)
	}
	return nil
}
func (c Chromium) withBrowser(ctx context.Context, fn func(*browser) error) error {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	if c.Leaser == nil {
		return ErrRenderer
	}
	executable := c.Executable
	if executable == "" {
		for _, name := range []string{"google-chrome", "chromium", "chromium-browser"} {
			p, err := proc.LookPath(name)
			if err == nil {
				executable = p
				break
			}
		}
	}
	if executable == "" {
		return ErrRenderer
	}
	profile, err := os.MkdirTemp("", "lina-mock-browser-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(profile)
	activity, err := c.Leaser.Acquire(ctx, lease.Activity{Class: lease.Live, Reason: "draw mock"})
	if err != nil {
		return err
	}
	defer activity.Release()
	log := &devtoolsLog{ready: make(chan string, 1)}
	owner := proc.NewOwner()
	done := make(chan error, 1)
	childCtx, stop := context.WithCancel(ctx)
	go func() {
		done <- owner.Run(childCtx, activity, proc.Command{Path: executable, Args: []string{"--headless=new", "--disable-gpu", "--disable-background-networking", "--disable-component-update", "--disable-extensions", "--disable-sync", "--no-first-run", "--no-default-browser-check", "--metrics-recording-only", "--remote-debugging-address=127.0.0.1", "--remote-debugging-port=0", "--user-data-dir=" + profile, "about:blank"}, Env: []string{"HOME=" + profile, "TMPDIR=" + profile, "PATH=/usr/bin:/bin", "LANG=C.UTF-8"}, Stdout: io.Discard, Stderr: log})
	}()
	exited := false
	defer func() {
		stop()
		if !exited {
			<-done
		}
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer closeCancel()
		_ = owner.Close(closeCtx)
	}()
	var endpoint string
	select {
	case endpoint = <-log.ready:
	case <-done:
		exited = true
		return ErrRenderer
	case <-ctx.Done():
		return ctx.Err()
	}
	conn, _, err := websocket.Dial(ctx, endpoint, nil)
	if err != nil {
		return err
	}
	defer conn.CloseNow()
	conn.SetReadLimit(32 << 20)
	b := &browser{conn: conn}
	var target struct{ TargetID string }
	if err = b.call(ctx, "Target.createTarget", map[string]any{"url": "about:blank"}, &target); err != nil {
		return err
	}
	var attached struct{ SessionID string }
	if err = b.call(ctx, "Target.attachToTarget", map[string]any{"targetId": target.TargetID, "flatten": true}, &attached); err != nil {
		return err
	}
	b.session = attached.SessionID
	for _, method := range []string{"Page.enable", "Runtime.enable"} {
		if err = b.call(ctx, method, map[string]any{}, nil); err != nil {
			return err
		}
	}
	return fn(b)
}

type frameTree struct {
	Frame       struct{ ID string }
	ChildFrames []frameTree
}

func (b *browser) loadFrame(ctx context.Context, html string, width int) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	lastStatus := "no child frame"
	b.frameSession = ""
	if err := b.call(ctx, "Emulation.setDeviceMetricsOverride", map[string]any{"width": width, "height": 900, "deviceScaleFactor": 1, "mobile": false}, nil); err != nil {
		return 0, err
	}
	var tree struct{ FrameTree frameTree }
	if err := b.call(ctx, "Page.getFrameTree", map[string]any{}, &tree); err != nil {
		return 0, err
	}
	if err := b.call(ctx, "Page.setDocumentContent", map[string]any{"frameId": tree.FrameTree.Frame.ID, "html": html}, nil); err != nil {
		return 0, err
	}
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		tree.FrameTree = frameTree{}
		if err := b.call(ctx, "Page.getFrameTree", map[string]any{}, &tree); err != nil {
			return 0, err
		}
		worldID := 0
		if len(tree.FrameTree.ChildFrames) == 1 {
			var world struct{ ExecutionContextID int }
			if err := b.call(ctx, "Page.createIsolatedWorld", map[string]any{"frameId": tree.FrameTree.ChildFrames[0].Frame.ID, "worldName": "lina-check"}, &world); err == nil {
				worldID = world.ExecutionContextID
				b.frameSession = ""
			} else {
				lastStatus = err.Error()
			}
		}
		if worldID == 0 {
			rootSession := b.session
			b.session = ""
			var targets struct {
				TargetInfos []struct{ TargetID, Type, URL string }
			}
			err := b.call(ctx, "Target.getTargets", map[string]any{}, &targets)
			if err == nil {
				for _, target := range targets.TargetInfos {
					if target.Type == "iframe" && target.URL == "about:srcdoc" {
						var attached struct{ SessionID string }
						if b.frameSession == "" {
							err = b.call(ctx, "Target.attachToTarget", map[string]any{"targetId": target.TargetID, "flatten": true}, &attached)
							if err == nil {
								b.frameSession = attached.SessionID
							}
						}
						if b.frameSession != "" {
							worldID = -1
						}
						break
					}
				}
			}
			b.session = rootSession
		}
		if worldID != 0 {
			var ready bool
			err := b.evaluate(ctx, worldID, `document.URL === 'about:srcdoc' && document.readyState === 'complete' && !!document.body`, &ready)
			if err == nil && ready {
				return worldID, nil
			}
			lastStatus = fmt.Sprintf("world=%d ready=%t err=%v", worldID, ready, err)
		}
		select {
		case <-ctx.Done():
			return 0, fmt.Errorf("frame readiness: %s: %w", lastStatus, ctx.Err())
		case <-tick.C:
		}
	}
}

func (c Chromium) Draw(ctx context.Context, fragment string, checklist []result.MockItem) (Rendered, error) {
	safe, static := CheckFragment(fragment, checklist)
	out := Rendered{}
	passed := map[string]bool{}
	for k, v := range static {
		passed[k] = v
	}
	if safe == "" {
		for _, id := range Dimensions {
			out.Checks = append(out.Checks, result.MockCheck{ID: id, Passed: passed[id]})
		}
		return out, nil
	}
	out.Page = generatedui.MockPage("Mock", safe)
	err := c.withBrowser(ctx, func(b *browser) error {
		passed["errors"] = true
		for _, width := range []int{390, 1280} {
			world, err := b.loadFrame(ctx, generatedui.MockFrame(out.Page, width), width)
			if err != nil {
				return err
			}
			raw, _ := json.Marshal(checklist)
			var checks struct{ Elements, Controls, Layout, Errors bool }
			expression := `(async()=>{await document.fonts.ready;const items=` + string(raw) + `;let elements=true,controls=true;for(const item of items){const el=document.querySelector('[data-check="'+item.id+'"]');if(!el||!el.getClientRects().length){elements=false;continue}const label=el.textContent+' '+(el.getAttribute('aria-label')||'')+' '+(el.getAttribute('placeholder')||'');if(!label.includes(item.description))elements=false;if(item.action==='toggle'){const target=document.getElementById(item.target);if(!target){controls=false;continue}const before=target.hidden;el.click();if(target.hidden===before)controls=false;el.click();if(target.hidden!==before)controls=false}else if(item.action==='select'){if(el.tagName==='SELECT'){if(el.options.length<2){controls=false;continue}const old=el.selectedIndex;el.selectedIndex=(old+1)%el.options.length;el.dispatchEvent(new Event('change',{bubbles:true}));if(el.selectedIndex===old)controls=false;el.selectedIndex=old}else if(el.type==='radio'){const old=el.checked;el.checked=false;el.click();if(!el.checked)controls=false;el.checked=old}else{const old=el.checked;el.click();if(el.checked===old)controls=false;el.checked=old}}else if(item.action==='input'){const old=el.value;el.value=el.type==='date'?'2026-01-01':el.type==='number'?'1':'sample';el.dispatchEvent(new Event('input',{bubbles:true}));if(!el.value)controls=false;el.value=old}}return{Elements:elements,Controls:controls,Errors:document.documentElement.dataset.linaReady==='true'&&!document.documentElement.dataset.linaError,Layout:document.documentElement.scrollWidth<=innerWidth&&document.body.scrollWidth<=innerWidth}})()`
			if err = b.evaluate(ctx, world, expression, &checks); err != nil {
				return err
			}
			passed["errors"] = passed["errors"] && checks.Errors
			passed["elements"] = passed["elements"] && checks.Elements
			passed["controls"] = passed["controls"] && checks.Controls
			passed[fmt.Sprintf("layout_%d", width)] = checks.Layout
			if width == 1280 {
				var capture struct{ Data string }
				if err = b.call(ctx, "Page.captureScreenshot", map[string]any{"format": "png", "captureBeyondViewport": false}, &capture); err != nil {
					return err
				}
				out.PNG, err = base64.StdEncoding.DecodeString(capture.Data)
				if err != nil {
					return err
				}
			}
		}
		passed["errors"] = passed["errors"] && b.exceptions == 0
		return nil
	})
	if err != nil {
		return Rendered{}, err
	}
	for _, id := range Dimensions {
		out.Checks = append(out.Checks, result.MockCheck{ID: id, Passed: passed[id]})
	}
	return out, nil
}
