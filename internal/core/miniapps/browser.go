// SPDX-License-Identifier: Apache-2.0
package miniapps

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
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/proc"
	"github.com/thisisjun786/lina/kit/result"
)

// ErrBrowser reports that no headless Chromium is available. A missing browser
// fails the pre-live check; it never substitutes a model score.
var ErrBrowser = errors.New("mini_app_browser_unavailable")

// Browser checks a served app at phone and desktop widths with installed
// Chromium. The app is loaded over its real origin so its policy applies.
type Browser struct {
	Executable string
	Leaser     lease.Leaser
}

type devtools struct {
	mu    sync.Mutex
	tail  string
	ready chan string
	sent  bool
}

func (d *devtools) Write(p []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.tail += string(p)
	if len(d.tail) > 8192 {
		d.tail = d.tail[len(d.tail)-8192:]
	}
	for _, line := range strings.Split(d.tail, "\n") {
		if _, after, ok := strings.Cut(line, "DevTools listening on "); ok && !d.sent && strings.HasPrefix(after, "ws://127.0.0.1:") {
			d.ready <- strings.TrimSpace(after)
			d.sent = true
		}
	}
	return len(p), nil
}

type cdp struct {
	conn       *websocket.Conn
	id         int
	session    string
	exceptions int
}

func (c *cdp) call(ctx context.Context, method string, params any, out any) error {
	c.id++
	request := map[string]any{"id": c.id, "method": method, "params": params}
	if c.session != "" {
		request["sessionId"] = c.session
	}
	raw, _ := json.Marshal(request)
	if err := c.conn.Write(ctx, websocket.MessageText, raw); err != nil {
		return err
	}
	for {
		_, raw, err := c.conn.Read(ctx)
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
			c.exceptions++
		}
		if reply.ID != c.id {
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

func (c *cdp) evaluate(ctx context.Context, expression string, out any) error {
	var r struct {
		Result           struct{ Value json.RawMessage }
		ExceptionDetails json.RawMessage
	}
	if err := c.call(ctx, "Runtime.evaluate", map[string]any{"expression": expression, "returnByValue": true, "awaitPromise": true}, &r); err != nil {
		return err
	}
	if len(r.ExceptionDetails) > 0 {
		return errors.New("mini_app_page_error")
	}
	if out != nil {
		return json.Unmarshal(r.Result.Value, out)
	}
	return nil
}

func (b Browser) withBrowser(ctx context.Context, fn func(*cdp) error) error {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if b.Leaser == nil {
		return ErrBrowser
	}
	executable := b.Executable
	if executable == "" {
		executable = os.Getenv("LINA_CHROMIUM")
	}
	if executable == "" {
		for _, name := range []string{"google-chrome", "chromium", "chromium-browser"} {
			if p, err := proc.LookPath(name); err == nil {
				executable = p
				break
			}
		}
	}
	if executable == "" {
		return ErrBrowser
	}
	profile, err := os.MkdirTemp("", "lina-miniapp-browser-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(profile)
	activity, err := b.Leaser.Acquire(ctx, lease.Activity{Class: lease.Live, Reason: "check mini app"})
	if err != nil {
		return err
	}
	defer activity.Release()
	log := &devtools{ready: make(chan string, 1)}
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
		return ErrBrowser
	case <-ctx.Done():
		return ctx.Err()
	}
	conn, _, err := websocket.Dial(ctx, endpoint, nil)
	if err != nil {
		return err
	}
	defer conn.CloseNow()
	conn.SetReadLimit(32 << 20)
	c := &cdp{conn: conn}
	var target struct{ TargetID string }
	if err = c.call(ctx, "Target.createTarget", map[string]any{"url": "about:blank"}, &target); err != nil {
		return err
	}
	var attached struct{ SessionID string }
	if err = c.call(ctx, "Target.attachToTarget", map[string]any{"targetId": target.TargetID, "flatten": true}, &attached); err != nil {
		return err
	}
	c.session = attached.SessionID
	for _, method := range []string{"Page.enable", "Runtime.enable"} {
		if err = c.call(ctx, method, map[string]any{}, nil); err != nil {
			return err
		}
	}
	return fn(c)
}

// CheckURL loads url at 390 and 1280 CSS pixels and reports every checklist
// control, page errors and horizontal overflow. It returns a desktop capture
// for the person when the app renders.
func (b Browser) CheckURL(ctx context.Context, url string, checklist []result.MockItem) (map[string]bool, []byte, error) {
	if result.ValidateMockChecklist(checklist) != nil {
		return nil, nil, ErrInvalid
	}
	passed := map[string]bool{"errors": true, "elements": true, "controls": true}
	var capture []byte
	err := b.withBrowser(ctx, func(c *cdp) error {
		if err := c.call(ctx, "Page.addScriptToEvaluateOnNewDocument", map[string]any{"source": `window.__linaErrors=0;addEventListener('error',()=>window.__linaErrors++);addEventListener('unhandledrejection',()=>window.__linaErrors++);`}, nil); err != nil {
			return err
		}
		raw, _ := json.Marshal(checklist)
		for _, width := range []int{390, 1280} {
			if err := c.call(ctx, "Emulation.setDeviceMetricsOverride", map[string]any{"width": width, "height": 900, "deviceScaleFactor": 1, "mobile": width == 390}, nil); err != nil {
				return err
			}
			var nav struct{ FrameID string }
			if err := c.call(ctx, "Page.navigate", map[string]any{"url": url}, &nav); err != nil {
				return err
			}
			if err := waitApp(ctx, c, checklist); err != nil {
				return err
			}
			var checks struct{ Elements, Controls, Layout, Errors bool }
			expression := `(async()=>{await document.fonts.ready;const items=` + string(raw) + `;let elements=true,controls=true;for(const item of items){const els=document.querySelectorAll('[data-check="'+item.id+'"]');if(els.length!==1||!els[0].getClientRects().length){elements=false;continue}const el=els[0];const label=el.textContent+' '+(el.getAttribute('aria-label')||'')+' '+(el.getAttribute('placeholder')||'');if(!label.includes(item.description))elements=false;if(item.action==='toggle'){const target=document.getElementById(item.target);if(!target){controls=false;continue}const before=target.hidden;el.click();if(target.hidden===before)controls=false;el.click();if(target.hidden!==before)controls=false}else if(item.action==='select'){if(el.tagName==='SELECT'){if(el.options.length<2)controls=false}else if(!(el.tagName==='INPUT'&&(el.type==='checkbox'||el.type==='radio'))){controls=false}}else if(item.action==='input'){if(el.tagName!=='TEXTAREA'&&!(el.tagName==='INPUT'&&['text','number','date'].includes(el.type)))controls=false}}return {Elements:elements,Controls:controls,Layout:document.documentElement.scrollWidth<=window.innerWidth+1,Errors:(window.__linaErrors||0)===0};})()`
			if err := c.evaluate(ctx, expression, &checks); err != nil {
				return err
			}
			passed["elements"] = passed["elements"] && checks.Elements
			passed["controls"] = passed["controls"] && checks.Controls
			passed["errors"] = passed["errors"] && checks.Errors
			passed[fmt.Sprintf("layout_%d", width)] = checks.Layout
			if width == 1280 {
				var shot struct{ Data string }
				if err := c.call(ctx, "Page.captureScreenshot", map[string]any{"format": "png", "captureBeyondViewport": false}, &shot); err != nil {
					return err
				}
				data, err := base64.StdEncoding.DecodeString(shot.Data)
				if err != nil {
					return err
				}
				capture = data
			}
		}
		passed["errors"] = passed["errors"] && c.exceptions == 0
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return passed, capture, nil
}

func waitApp(ctx context.Context, c *cdp, checklist []result.MockItem) error {
	raw, _ := json.Marshal(checklist)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var ready bool
		expression := `(async()=>{if(document.readyState!=='complete')return false;await document.fonts.ready;const items=` + string(raw) + `;return items.every(item=>document.querySelectorAll('[data-check="'+item.id+'"]').length===1);})()`
		if err := c.evaluate(ctx, expression, &ready); err == nil && ready {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	return errors.New("mini_app_not_ready")
}
