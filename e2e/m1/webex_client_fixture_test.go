// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/kit/protocol"
)

// The messenger control flow needs a decider, and Core refuses a connection
// from its own process tree. Core runs in the scenario process for the Webex
// origin seam, so the client is started through a short-lived shell that exits
// at once: the client is then reparented out of Core's tree, as the person's
// own app is.

// webexClientResult is what the child reports to the scenario.
type webexClientResult struct {
	Status       string            `json:"status"`
	Platform     string            `json:"platform"`
	Authorize    string            `json:"authorize"`
	Sources      []webexSource `json:"sources"`
	Disconnected *string           `json:"disconnected"`
	Error        string            `json:"error"`
}

type webexSource struct {
	Platform string `json:"platform"`
	Account  string `json:"account"`
	State    string `json:"state"`
}

// webexClientFixture is the child process. LINA_WEBEX_OP selects what
// it does; it prints one JSON result and exits.
func webexClientFixture() {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	// The launching shell exits immediately. Wait until this process no longer
	// reports it as the parent, so the connection is made from outside Core's
	// process tree.
	parent := os.Getppid()
	for deadline := time.Now().Add(2 * time.Second); os.Getppid() == parent && time.Now().Before(deadline); {
		time.Sleep(time.Millisecond)
	}
	result, err := webexClientRun(ctx, os.Getenv("LINA_WEBEX_SOCKET"), os.Getenv("LINA_WEBEX_OP"))
	if err != nil {
		result.Error = err.Error()
	}
	path := os.Getenv("LINA_WEBEX_RESULT")
	if path == "" {
		_ = json.NewEncoder(os.Stdout).Encode(result)
		os.Exit(0)
	}
	data, err := json.Marshal(result)
	if err != nil {
		os.Exit(1)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func webexClientRun(ctx context.Context, socket, op string) (webexClientResult, error) {
	c, err := clientapi.DialWithCapabilities(ctx, socket, "test", []protocol.Capability{
		{Name: "sign_in_relay", Version: "1.0"}, {Name: "webex", Version: "1.0"},
	})
	if err != nil {
		return webexClientResult{}, err
	}
	defer c.Close()
	send := func(p protocol.WebexRequest) (protocol.Envelope, error) {
		if _, err := c.Messenger(ctx, p); err != nil {
			return protocol.Envelope{}, err
		}
		return c.Receive(ctx)
	}
	platform := "webex"
	result := webexClientResult{Platform: platform}
	switch op {
	case "setup":
		clientID, clientSecret := "synthetic-client", "synthetic-secret"
		frame, err := send(protocol.WebexRequest{Operation: new("configure"), Platform: &platform, ClientID: &clientID, ClientSecret: &clientSecret})
		if err != nil {
			return result, err
		}
		if configured, ok := frame.Payload.(protocol.WebexResult); !ok || configured.Status == nil || *configured.Status != "configured" {
			return result, fmt.Errorf("configure: %v", frame.Error)
		}
		listener, err := clientapi.ListenSignIn(ctx)
		if err != nil {
			return result, err
		}
		defer listener.Close()
		if _, err := c.Messenger(ctx, protocol.WebexRequest{Operation: new("sign_in"), Platform: &platform}); err != nil {
			return result, err
		}
		if frame, err = c.Receive(ctx); err != nil || frame.Kind != protocol.KindSignInListen {
			return result, fmt.Errorf("browser relay: %v %v", err, frame.Error)
		}
		port := json.Number(strconv.Itoa(listener.Port()))
		if _, err := c.SignInListening(ctx, protocol.SignInListening{Port: &port}); err != nil {
			return result, err
		}
		if frame, err = c.Receive(ctx); err != nil {
			return result, err
		}
		open, ok := frame.Payload.(protocol.SignInOpen)
		if !ok || open.URL == nil {
			return result, fmt.Errorf("authorization URL: %v", frame.Error)
		}
		result.Authorize = *open.URL
		// The person's browser: Webex redirects to the client's loopback
		// listener, which relays the redirect back to Core.
		response, err := http.Get(*open.URL)
		if err != nil {
			return result, err
		}
		response.Body.Close()
		select {
		case redirect := <-listener.Redirects():
			if _, err := c.SignInRedirect(ctx, redirect); err != nil {
				return result, err
			}
		case <-ctx.Done():
			return result, ctx.Err()
		}
		if frame, err = c.Receive(ctx); err != nil || frame.Error != nil {
			return result, fmt.Errorf("sign-in: %v %v", err, frame.Error)
		}
		signedIn, ok := frame.Payload.(protocol.WebexResult)
		if !ok || signedIn.Status == nil {
			return result, fmt.Errorf("sign-in result: %T", frame.Payload)
		}
		result.Status = *signedIn.Status
		if signedIn.Platform != nil {
			result.Platform = *signedIn.Platform
		}
		return result, nil
	case "finish":
		frame, err := send(protocol.WebexRequest{Operation: new("list")})
		if err != nil {
			return result, err
		}
		listed, ok := frame.Payload.(protocol.WebexResult)
		if !ok || listed.Sources == nil {
			return result, fmt.Errorf("list: %v", frame.Error)
		}
		for _, row := range *listed.Sources {
			result.Sources = append(result.Sources, webexSource{Platform: row.Platform, Account: row.Account, State: row.State})
		}
		if len(result.Sources) == 0 {
			return result, fmt.Errorf("no sources listed")
		}
		account := result.Sources[0].Account
		frame, err = send(protocol.WebexRequest{Operation: new("disconnect"), Account: &account})
		if err != nil {
			return result, err
		}
		disconnected, ok := frame.Payload.(protocol.WebexResult)
		if !ok || disconnected.Status == nil {
			return result, fmt.Errorf("disconnect: %v", frame.Error)
		}
		result.Disconnected = disconnected.Status
		return result, nil
	}
	return result, fmt.Errorf("unknown messenger client operation %q", op)
}

// runMessengerClient runs the child client for one operation and returns its
// result. The child is outside Core's process tree, which is what lets it act
// as the person's decider.
func runMessengerClient(t *testing.T, socket, op string) webexClientResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	result := filepath.Join(t.TempDir(), "result.json")
	// The shell exits at once and leaves the client reparented, outside Core's
	// process tree. The client writes its one result and exits.
	command := exec.CommandContext(ctx, "sh", "-c", `"$0" >/dev/null 2>&1 &`, os.Args[0])
	command.Env = append(os.Environ(),
		"LINA_WEBEX_SOCKET="+socket,
		"LINA_WEBEX_OP="+op,
		"LINA_WEBEX_RESULT="+result,
	)
	if err := command.Run(); err != nil {
		t.Fatalf("messenger client %s launcher: %v", op, err)
	}
	var data []byte
	for {
		raw, err := os.ReadFile(result)
		if err == nil && len(raw) > 0 {
			data = raw
			break
		}
		if ctx.Err() != nil {
			t.Fatalf("messenger client %s produced no result", op)
		}
		time.Sleep(5 * time.Millisecond)
	}
	var out webexClientResult
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("messenger client %s result %q: %v", op, data, err)
	}
	if out.Error != "" {
		t.Fatalf("messenger client %s: %s", op, strings.TrimSpace(out.Error))
	}
	return out
}
