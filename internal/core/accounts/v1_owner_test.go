// SPDX-License-Identifier: Apache-2.0

package accounts_test

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/accounts"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/secrets"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/provider"
)

var v1Owner = flag.Bool("v1-owner-signin", false, "run the real owner-assisted ChatGPT acceptance check")
var v1State = flag.String("v1-state-root", "", "private absolute state directory outside the checkout")
var v1Output = flag.String("v1-report", "", "new private observation JSON file outside the checkout")
var v1Mode = flag.String("v1-mode", "local", "local loopback listener or pasted redirect")
var v1Model = flag.String("v1-model", "", "model slug from the signed-in account catalog; empty selects the first listed model")

type v1Leaser struct{}

func (v1Leaser) Acquire(context.Context, lease.Activity) (lease.Lease, error) {
	return lease.New(func() {}), nil
}

// This command is skipped in ordinary verification. Its network calls run only
// after the owner explicitly selects the flag and authorizes LINA in a browser.
func TestV1OwnerSignIn(t *testing.T) {
	if !*v1Owner {
		t.Skip("requires owner ChatGPT sign-in")
	}
	if *v1Mode != "local" && *v1Mode != "pasted" {
		t.Fatal("v1-mode must be local or pasted")
	}
	if !filepath.IsAbs(*v1State) || !filepath.IsAbs(*v1Output) {
		t.Fatal("state and observation paths must be absolute")
	}
	_, source, _, _ := runtime.Caller(0)
	checkout, err := filepath.Abs(filepath.Join(filepath.Dir(source), "../../.."))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{*v1State, *v1Output} {
		relative, err := filepath.Rel(checkout, path)
		if err != nil || relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			t.Fatal("private acceptance files must be outside the checkout")
		}
	}
	if err := os.MkdirAll(*v1State, 0700); err != nil {
		t.Fatal("cannot create private acceptance state")
	}
	info, err := os.Lstat(*v1State)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		t.Fatal("acceptance state must be a private directory")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Minute)
	defer cancel()
	controls, err := ctlfiles.Open(*v1State)
	if err != nil {
		t.Fatal("cannot open acceptance controls")
	}
	defer controls.Close()
	if err := controls.Acquire(); err != nil {
		t.Fatal("acceptance state is in use")
	}
	migrations := canon.Migrations()
	db, err := canon.Open(ctx, canon.Config{Path: filepath.Join(*v1State, "canon.sqlite"), Epoch: 1, Controls: controls, Migrations: migrations})
	if err != nil {
		t.Fatal("cannot open acceptance canon")
	}
	defer db.Close(context.Background())
	vault, err := secrets.OpenFiles(*v1State)
	if err != nil {
		t.Fatal("cannot open acceptance secret storage")
	}
	defer vault.Close()
	leaser := v1Leaser{}
	store, err := accounts.Open(ctx, db, vault, accounts.ChatGPTRefresher(leaser, nil))
	if err != nil {
		t.Fatal("cannot open account store")
	}
	defer store.Close()
	signer, err := accounts.NewSignIn(ctx, accounts.SignInConfig{Store: store, Person: "v1-owner", Leaser: leaser})
	if err != nil {
		t.Fatal("cannot initialize sign-in")
	}
	defer signer.Close()
	relay := clientsrv.NewSignInRelay("v1-owner", signer)
	caps := []protocol.Capability{clientsrv.AccountsCapability, clientsrv.SignInCapability}
	rows := clientsrv.LocalCombinations()
	for i := range rows {
		rows[i].Capabilities = caps
		rows[i].Evidence = []string{"go test ./e2e/m1 -run TestM1ChatGPTSignInOwnerBinding"}
	}
	// A short temporary socket path avoids platform limits. Credentials remain in
	// the explicitly selected private state root.
	socketRoot, err := os.MkdirTemp("", "lina-v1-")
	if err != nil {
		t.Fatal("cannot create socket directory")
	}
	defer os.RemoveAll(socketRoot)
	listener, err := clientsrv.Listen(filepath.Join(socketRoot, "run", "client.sock"))
	if err != nil {
		t.Fatal("cannot start acceptance client service")
	}
	serving, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		done <- clientsrv.Serve(serving, listener, clientsrv.Identity{MainID: "v1-main", PersonID: "v1-owner", Epoch: "1", Supported: rows, Operations: relay.Operations()})
	}()
	defer func() { stop(); <-done }()
	conn, err := net.Dial("unix", listener.Addr().String())
	if err != nil {
		t.Fatal("cannot connect acceptance relay")
	}
	defer conn.Close()
	client := &v1Client{conn: conn, reader: bufio.NewReaderSize(conn, 1<<20), t: t}
	peer, platform := "lina-tui", rows[0].Platform
	client.call(protocol.KindHello, protocol.Hello{Party: protocol.PartyClient, ID: "v1-client", Peer: &peer, Platform: &platform, ProductVersion: "v1-acceptance", ProtocolVersions: []string{"1.0"}, Capabilities: caps})
	operation, providerID, purpose := "sign_in", "chatgpt", "login"
	answer := client.call(protocol.KindAccountRequest, protocol.AccountRequest{Operation: &operation, Provider: &providerID, Purpose: &purpose})
	if answer.Kind != protocol.KindSignInListen {
		t.Fatal("Core did not request a listener")
	}
	report := map[string]any{"check": "V1 ChatGPT sign-in and Responses round trip", "status": "partial_live_observation", "mode": *v1Mode, "observed_at": time.Now().UTC().Format(time.RFC3339), "sign_in": map[string]any{"dynamic_registration": nil, "loopback_redirect": nil, "pasted_redirect": nil, "granted_scope_check": nil, "id_token_verification": nil, "refresh_rotation": nil}, "responses": nil, "remote_client": nil}
	output, err := os.OpenFile(*v1Output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal("observation file must be new and writable")
	}
	defer func() {
		defer output.Close()
		encoder := json.NewEncoder(output)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(report); err != nil {
			t.Error("cannot write observation")
		}
		output.Sync()
	}()
	var callback string
	var incoming <-chan string
	listening := protocol.SignInListening{}
	if *v1Mode == "local" {
		callbackListener, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal("cannot open browser callback listener")
		}
		port := json.Number(strconv.Itoa(callbackListener.Addr().(*net.TCPAddr).Port))
		listening.Port = &port
		redirectBase := "http://" + callbackListener.Addr().String() + accounts.SignInCallbackPath
		captured := make(chan string, 1)
		incoming = captured
		server := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet || r.URL.Path != accounts.SignInCallbackPath {
				http.NotFound(w, r)
				return
			}
			select {
			case captured <- redirectBase + "?" + r.URL.RawQuery:
			default:
			}
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprintln(w, "Return to the LINA acceptance command.")
		})}
		callbackDone := make(chan struct{})
		go func() { defer close(callbackDone); _ = server.Serve(callbackListener) }()
		defer func() { server.Close(); <-callbackDone }()
	} else {
		reason := "remote_terminal"
		listening.CannotListen = &reason
	}
	opened := client.call(protocol.KindSignInListening, listening)
	open, ok := opened.Payload.(protocol.SignInOpen)
	if !ok || open.URL == nil {
		t.Fatal("Core did not return a sign-in URL")
	}
	fmt.Println("Open this LINA authorization URL in your system browser:")
	fmt.Println(*open.URL)
	if incoming != nil {
		select {
		case callback = <-incoming:
		case <-ctx.Done():
			t.Fatal("owner sign-in timed out")
		}
	} else {
		fmt.Println("Paste the complete redirected URL, then press Enter (the input is not recorded):")
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil {
			t.Fatal("cannot read pasted redirect")
		}
		callback = strings.TrimSpace(line)
	}
	completed := client.call(protocol.KindSignInRedirect, protocol.SignInRedirect{URL: &callback})
	result, ok := completed.Payload.(protocol.AccountResult)
	if !ok || result.AccountID == nil || result.Status == nil || *result.Status != "ok" {
		t.Fatal("sign-in did not produce a usable account")
	}
	signin := report["sign_in"].(map[string]any)
	signin["dynamic_registration"], signin["granted_scope_check"], signin["id_token_verification"] = true, true, true
	if *v1Mode == "local" {
		signin["loopback_redirect"] = true
	} else {
		signin["pasted_redirect"] = true
	}
	id := provider.AccountID(*result.AccountID)
	if err := store.Refresh(ctx, id); err != nil {
		t.Fatal("refresh rotation failed")
	}
	credential, err := store.Credential(ctx, id)
	if err != nil {
		t.Fatal("persisted rotated credential unavailable")
	}
	signin["refresh_rotation"] = true
	model, err := v1SelectModel(ctx, credential, *v1Model)
	if err != nil {
		t.Fatal("account model catalog unavailable or selected model not listed")
	}
	observed, err := probeV1Responses(ctx, credential, model, nil)
	report["responses"] = observed
	report["model"] = model
	if err != nil {
		t.Fatal(err)
	}
	fmt.Println("Local observation written. Remote relay and combined V1 acceptance still require their own evidence; do not infer them from this run.")
}

type v1Client struct {
	conn   net.Conn
	reader *bufio.Reader
	t      *testing.T
	seq    int
	epoch  string
}

func (c *v1Client) call(kind protocol.Kind, payload protocol.Payload) protocol.Envelope {
	c.t.Helper()
	c.seq++
	id := strconv.Itoa(c.seq)
	epoch := c.epoch
	if epoch == "" {
		epoch = "0"
	}
	request := protocol.Envelope{Protocol: protocol.Protocol{Name: "client", Version: "1.0"}, Kind: kind, MessageID: id, AttemptID: id, Sender: protocol.Sender{Party: protocol.PartyClient, ID: "v1-client"}, Epoch: epoch, SentAt: time.Now().UTC().Format(time.RFC3339Nano), EffectState: protocol.EffectNone, SourceRefs: []protocol.SourceRef{}, Payload: payload}
	c.conn.SetDeadline(time.Now().Add(time.Minute))
	if err := json.NewEncoder(c.conn).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "method": kind, "params": request}); err != nil {
		c.t.Fatal("cannot send relay request")
	}
	line, err := c.reader.ReadSlice('\n')
	if err != nil {
		c.t.Fatal("cannot receive relay response")
	}
	var answer struct {
		JSONRPC, ID string
		Result      protocol.Envelope
	}
	if json.Unmarshal(line, &answer) != nil || answer.JSONRPC != "2.0" || answer.ID != id || answer.Result.ReplyTo == nil || *answer.Result.ReplyTo != id || answer.Result.Sender.Party != protocol.PartyCore || answer.Result.Sender.ID != "v1-main" {
		c.t.Fatal("invalid relay correlation")
	}
	if answer.Result.Error != nil {
		c.t.Fatalf("relay refused: %s", answer.Result.Error.Code)
	}
	c.epoch = answer.Result.Epoch
	return answer.Result
}
func v1SelectModel(ctx context.Context, c provider.Credential, selected string) (provider.ModelRef, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.openai.com/v1/models", nil)
	if err != nil {
		return "", errV1Unavailable
	}
	if err = c.Authorize(request); err != nil {
		return "", errV1Unavailable
	}
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return "", errV1Unavailable
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return "", errV1Rejected
	}
	var catalog struct {
		Models []struct{ Slug, Visibility string } `json:"models"`
	}
	if json.NewDecoder(http.MaxBytesReader(nil, response.Body, 1<<20)).Decode(&catalog) != nil {
		return "", errV1Unavailable
	}
	for _, model := range catalog.Models {
		if model.Visibility == "list" && model.Slug != "" && (selected == "" || selected == model.Slug) {
			return provider.ModelRef(model.Slug), nil
		}
	}
	return "", errV1Incomplete
}
