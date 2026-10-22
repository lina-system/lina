// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/core/boot"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestM1TUIAccountsInstalledCore(t *testing.T) {
	raw, err := os.ReadFile("m1/tui-accounts-installed.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct{ Subject, Code, ClientID, Input string }
	var wire struct {
		Subject  string `json:"subject"`
		Code     string `json:"code"`
		ClientID string `json:"client_id"`
		Input    string `json:"input"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	scenario.Subject, scenario.Code, scenario.ClientID, scenario.Input = wire.Subject, wire.Code, wire.ClientID, wire.Input
	h, start := installedConversationHarness(t, "")
	core := start(false)
	client, err := clientapi.DialWithCapabilities(h.ctx, h.socket, "test", []protocol.Capability{{Name: "accounts", Version: "1.0"}, {Name: "sign_in_relay", Version: "1.0"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(client.Welcome.Capabilities) != 2 {
		t.Fatal("installed Core does not advertise accounts and sign-in relay without model configuration")
	}
	client.Close()
	terminal := h.startScreenTUI("TERM=xterm-256color", "SSH_CONNECTION=synthetic")
	waitScreen(t, h, terminal, "/signin")
	h.keys(terminal, scenario.Input+"\r")
	waitScreen(t, h, terminal, design.Text(design.English, "needs_sign_in"))
	h.keys(terminal, "\x04")
	terminal.process.wait(t, 0)
	h.expectTerminalRestored(terminal)
	h.stopCore(core)

	// The installed state and the exact production composition run again with
	// only the issuer HTTP transport injected. No product URL override exists.
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	wires := &signInWires{key: key, grants: make(map[string]map[string]any)}
	ctx, cancel := context.WithCancel(h.ctx)
	defer cancel()
	ready, done := make(chan struct{}), make(chan error, 1)
	socket := h.socket
	go func() {
		done <- boot.Run(ctx, boot.Config{StateRoot: h.root, Socket: socket, SignInTransport: wires, AfterStep: func(step boot.Step, _ boot.Snapshot) error {
			if step == boot.Ready {
				close(ready)
			}
			return nil
		}})
	}()
	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("installed composition: %v", err)
	case <-h.ctx.Done():
		t.Fatal(h.ctx.Err())
	}
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
			t.Error(err)
		}
	})
	tap := newAccountTap(t, h, socket)
	h.socket = tap.socket
	terminal = h.startScreenTUI("TERM=xterm-256color", "SSH_CONNECTION=synthetic")
	waitScreen(t, h, terminal, "/signin")
	h.keys(terminal, "/accounts\r")
	first := tap.result(t, h, "list")
	if first.Accounts == nil || len(*first.Accounts) != 0 {
		t.Fatal("fresh install has an account")
	}
	waitScreen(t, h, terminal, design.Text(design.English, "account.help"))
	h.keys(terminal, "s")
	authorization := tap.authorization(t, h)
	waitScreen(t, h, terminal, design.Text(design.English, "sign_in_open"))
	parsed, err := url.Parse(authorization)
	if err != nil {
		t.Fatal(err)
	}
	q := parsed.Query()
	wires.mu.Lock()
	wires.grants[scenario.Code] = map[string]any{"iss": "https://auth.openai.com", "aud": scenario.ClientID, "sub": scenario.Subject, "nonce": q.Get("nonce"), "iat": time.Now().Unix() - 1, "exp": time.Now().Add(time.Hour).Unix()}
	wires.mu.Unlock()
	callback := q.Get("redirect_uri") + "?" + url.Values{"code": {scenario.Code}, "state": {q.Get("state")}, "iss": {"https://auth.openai.com"}, "client_id": {scenario.ClientID}}.Encode()
	h.keys(terminal, "\x1b[200~"+callback+"\x1b[201~\r")
	signed := tap.result(t, h, "sign_in")
	listed := tap.result(t, h, "list")
	if signed.Status == nil || *signed.Status != "ok" || listed.Accounts == nil || len(*listed.Accounts) != 1 {
		t.Fatal("account not listed after sign-in")
	}
	id := (*listed.Accounts)[0].AccountID
	if signed.AccountID == nil || *signed.AccountID != id {
		t.Fatal("list changed account identity")
	}
	waitScreen(t, h, terminal, design.Text(design.English, "ok"))
	assertNoAccountTokens(t, h.root, tap.bytes(), terminal.screen.Bytes())
	readback, err := canon.OpenSQLiteReader(h.ctx, filepath.Join(h.root, "canon", "canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer readback.Close()
	var before string
	if err := readback.QueryRowContext(h.ctx, "SELECT subject FROM owner_binding WHERE singleton=1").Scan(&before); err != nil || before != scenario.Subject {
		t.Fatal("owner binding missing", err)
	}
	h.keys(terminal, "x")
	signedOut := tap.result(t, h, "sign_out")
	listed = tap.result(t, h, "list")
	if signedOut.Status == nil || *signedOut.Status != "needs_sign_in" || listed.Accounts == nil || len(*listed.Accounts) != 1 || (*listed.Accounts)[0].Status != "needs_sign_in" {
		t.Fatal("sign-out did not change account status")
	}
	waitScreen(t, h, terminal, design.Text(design.English, "needs_sign_in"))
	var after string
	if err := readback.QueryRowContext(h.ctx, "SELECT subject FROM owner_binding WHERE singleton=1").Scan(&after); err != nil || before != after {
		t.Fatal("sign-out changed owner binding", err)
	}
	files, err := os.ReadDir(filepath.Join(h.root, "secrets"))
	if err != nil || len(files) != 0 {
		t.Fatal("sign-out retained secret files", err)
	}
	h.keys(terminal, "\x04")
	terminal.process.wait(t, 0)
	h.expectTerminalRestored(terminal)
	assertNoAccountTokens(t, h.root, tap.bytes(), terminal.screen.Bytes(), terminal.process.stderr.Bytes())
}

// Transparent socket capture observes both directions without changing Core
// messages. It proves no provider token reaches the real TUI client.
type accountTap struct {
	socket  string
	close   func()
	mu      sync.Mutex
	raw     bytes.Buffer
	replies chan protocol.Envelope
}

func newAccountTap(t *testing.T, h *harness, target string, notifications ...bool) *accountTap {
	t.Helper()
	tap := &accountTap{socket: filepath.Join(filepath.Dir(target), "tui-tap.sock"), replies: make(chan protocol.Envelope, 64)}
	listener, err := net.Listen("unix", tap.socket)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	// A restarted Core closes the current peer; accept the TUI's reconnect
	// through the same tap so protocol barriers can cover restart scenarios too.
	serve := func(local net.Conn) {
		defer local.Close()
		// Do not turn a temporarily absent Core into a successful connection
		// followed by a fatal handshake EOF. Hold the accepted peer until the real
		// socket is available; only a successful dial establishes readiness.
		var remote net.Conn
		for {
			var err error
			remote, err = (&net.Dialer{}).DialContext(h.ctx, "unix", target)
			if err == nil {
				break
			}
			select {
			case <-h.ctx.Done():
				return
			case <-time.After(10 * time.Millisecond):
			}
		}
		defer remote.Close()
		stop := context.AfterFunc(h.ctx, func() { local.Close(); remote.Close() })
		defer stop()
		var wg sync.WaitGroup
		pump := func(dst, src net.Conn, reply bool) {
			defer wg.Done()
			defer dst.Close()
			scanner := bufio.NewScanner(src)
			scanner.Buffer(make([]byte, 4096), 1<<20)
			for scanner.Scan() {
				line := append(bytes.Clone(scanner.Bytes()), '\n')
				tap.mu.Lock()
				tap.raw.Write(line)
				tap.mu.Unlock()
				if reply {
					var rpc struct{ Result, Params json.RawMessage }
					if json.Unmarshal(line, &rpc) == nil {
						if len(rpc.Result) == 0 && len(notifications) > 0 && notifications[0] {
							rpc.Result = rpc.Params
						}
						if len(rpc.Result) == 0 {
							if _, err := dst.Write(line); err != nil {
								return
							}
							continue
						}
						var decoder protocol.ClientDecoder
						e, err := decoder.Decode(rpc.Result)
						if err == nil && e != nil {
							select {
							case tap.replies <- *e:
							case <-h.ctx.Done():
								return
							}
						}
					}
				}
				if _, err := dst.Write(line); err != nil {
					return
				}
			}
		}
		wg.Add(2)
		go pump(remote, local, false)
		go pump(local, remote, true)
		wg.Wait()
	}
	go func() {
		defer close(done)
		for {
			local, err := listener.Accept()
			if err != nil {
				return
			}
			serve(local)
		}
	}()
	var once sync.Once
	tap.close = func() { once.Do(func() { listener.Close(); <-done }) }
	t.Cleanup(tap.close)
	return tap
}
func (tap *accountTap) bytes() []byte {
	tap.mu.Lock()
	defer tap.mu.Unlock()
	return bytes.Clone(tap.raw.Bytes())
}
func (tap *accountTap) next(t *testing.T, h *harness) protocol.Envelope {
	t.Helper()
	select {
	case e := <-tap.replies:
		return e
	case <-h.ctx.Done():
		t.Fatal("missing account response")
		return protocol.Envelope{}
	}
}
func (tap *accountTap) result(t *testing.T, h *harness, operation string) protocol.AccountResult {
	t.Helper()
	for {
		e := tap.next(t, h)
		if e.Error != nil {
			t.Fatalf("account refusal: %s", e.Error.Code)
		}
		if p, ok := e.Payload.(protocol.AccountResult); ok && p.Operation != nil && *p.Operation == operation {
			return p
		}
	}
}
func (tap *accountTap) authorization(t *testing.T, h *harness) string {
	t.Helper()
	for {
		e := tap.next(t, h)
		if e.Error != nil {
			t.Fatalf("sign-in refusal: %s", e.Error.Code)
		}
		if p, ok := e.Payload.(protocol.SignInOpen); ok && p.URL != nil {
			return *p.URL
		}
	}
}
func assertNoAccountTokens(t *testing.T, root string, buffers ...[]byte) {
	t.Helper()
	check := func(data []byte) {
		for _, token := range []string{"synthetic-access", "synthetic-refresh"} {
			if bytes.Contains(data, []byte(token)) {
				t.Fatal("provider token escaped the secret store")
			}
		}
	}
	for _, data := range buffers {
		check(data)
	}
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if path == filepath.Join(root, "secrets") {
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		if strings.HasPrefix(path, filepath.Join(root, "system", "bin")+string(os.PathSeparator)) {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		data, err := io.ReadAll(f)
		if err == nil {
			check(data)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
