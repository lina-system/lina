// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/core/messenger/webex"
	"github.com/thisisjun786/lina/kit/protocol"
)

// webexClientResult is what the child reports to the scenario.
type webexClientResult struct {
	Status       string        `json:"status"`
	Platform     string        `json:"platform"`
	Authorize    string        `json:"authorize"`
	Sources      []webexSource `json:"sources"`
	Disconnected *string       `json:"disconnected"`
	Error        string        `json:"error"`
}

type webexSource struct {
	Platform string `json:"platform"`
	Account  string `json:"account"`
	State    string `json:"state"`
}

func webexClientRun(ctx context.Context, socket, op, browserBase string) (webexClientResult, error) {
	c, err := clientapi.DialWithCapabilities(ctx, socket, "test", []protocol.Capability{
		{Name: "sign_in_relay", Version: "1.0"}, {Name: "webex", Version: "1.0"},
	})
	if err != nil {
		return webexClientResult{}, err
	}
	defer c.Close()
	send := func(p protocol.WebexRequest) (protocol.Envelope, error) {
		if _, err := c.Webex(ctx, p); err != nil {
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
		if _, err := c.Webex(ctx, protocol.WebexRequest{Operation: new("sign_in"), Platform: &platform}); err != nil {
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
		response, err := http.Get(strings.Replace(*open.URL, webex.DefaultBaseURL, browserBase, 1))
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
