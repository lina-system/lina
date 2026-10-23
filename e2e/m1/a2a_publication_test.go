// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/core/a2a"
	"github.com/thisisjun786/lina/internal/core/boot"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/modelpath"
	"github.com/thisisjun786/lina/internal/core/secrets"
	"github.com/thisisjun786/lina/internal/core/worker/dispatch"
	"github.com/thisisjun786/lina/kit/protocol"
)

// The installed test executable runs the actual Core composition. Only the
// physical tailnet bind is mapped to loopback; TLS, tailscaled verification,
// issuer introspection, provider, task journals and worker processes are real.
func TestA2ACoreProcess(t *testing.T) {
	root := os.Getenv("LINA_A2A_TEST_ROOT")
	if root == "" {
		return
	}
	route, err := modelpath.ResponsesFromEnv(os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	ca, err := os.ReadFile(os.Getenv("LINA_A2A_TEST_CA"))
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		t.Fatal("invalid fixture CA")
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}
	defer transport.CloseIdleConnections()
	cfg := boot.Config{StateRoot: root, Socket: os.Getenv("LINA_A2A_TEST_SOCKET"), Conversation: route, A2A: &boot.A2AOptions{Transport: transport, Listen: func(ctx context.Context, network, address string) (net.Listener, error) {
		if network != "tcp" || address != "100.100.0.1:9443" {
			return nil, fmt.Errorf("unexpected A2A bind")
		}
		l, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
		if err != nil {
			return nil, err
		}
		if err = os.WriteFile(filepath.Join(root, "endpoint"), []byte("https://"+l.Addr().String()+"/a2a/published"), 0600); err != nil {
			l.Close()
			return nil, err
		}
		return l, nil
	}}}
	if err = boot.Run(ctx, cfg); err != nil {
		t.Fatal(err)
	}
}

func TestM3A2APublicationInstalledCore(t *testing.T) {
	for _, mode := range []string{"verified", "unverified", "denied", "uncertain-cancel", "revoked-running"} {
		t.Run(mode, func(t *testing.T) { testM3A2APublication(t, mode) })
	}
}
func testM3A2APublication(t *testing.T, mode string) {
	var scenario struct {
		Task, Input, Artifact string
		Cases                 []string
	}
	raw, err := os.ReadFile("../m3/a2a-publication.json")
	if err != nil || json.Unmarshal(raw, &scenario) != nil {
		t.Fatal("scenario", err)
	}
	fixture := t.TempDir()
	repo := filepath.Join(fixture, "repo")
	bin := filepath.Join(fixture, "bin")
	for _, dir := range []string{repo, bin} {
		if err = os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + fixture, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git: %v %s", err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-b", "dev")
	if err = os.WriteFile(filepath.Join(repo, "design.md"), []byte("# Result\nProduce result.txt.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "design.md")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "Initial design")
	git("remote", "add", "origin", "https://github.com/fixture/project.git")
	for name, source := range map[string]string{"agy": "../m3/antigravity-worker.mjs", "gh": "../m2/delivery-github.mjs"} {
		body, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(bin, name), body, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if mode == "denied" || mode == "uncertain-cancel" || mode == "revoked-running" {
		body, err := os.ReadFile("../m3/a2a-worker.mjs")
		if err != nil {
			t.Fatal(err)
		}
		name := "agy"
		if mode == "denied" {
			name = "codex"
		}
		if err = os.WriteFile(filepath.Join(bin, name), body, 0700); err != nil {
			t.Fatal(err)
		}
	}
	def := a2a.Definition{ID: scenario.Task, Name: "Write result", Description: "Create and verify the approved result file", Seconds: 120, MaxCalls: 4, Tools: []a2a.Tool{{Name: "write_record", Arguments: json.RawMessage(`{"id":"","revision":"","kind":"memo","title":"A2A task tool evidence","note":"The permitted Core tool ran."}`)}}, Work: dispatch.Definition{Worker: "antigravity", Repository: repo, Base: "dev", Branch: "a2a-result", What: "Create result.txt", Why: "Complete the published task", Scope: []string{"result.txt"}, NotTouch: []string{"design.md"}, Done: "The committed result.txt contains the requested result, with green checks and verified review", DesignRefs: []string{"design.md#result"}, WriteArea: []string{"result.txt"}}, Artifact: a2a.ArtifactRule{Path: "result.txt", MediaType: "text/plain", Contains: scenario.Artifact, MaxBytes: 4096}}
	if mode == "denied" {
		def.Work.Worker = "codex"
	}
	var calls atomic.Int32
	provider := httptest.NewServer(toolCatalogModel(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		if scriptedCodingManager(t, w, json.RawMessage(raw)) {
			return
		}
		var req struct{ Input []json.RawMessage }
		json.Unmarshal(raw, &req)
		if mode == "unverified" {
			calls.Add(1)
			emitResponse(w, "The task is complete and the file is committed.", true)
			return
		}
		switch calls.Add(1) {
		case 1:
			emitPersonaCall(w, "permitted-tool", "write_record", json.RawMessage(def.Tools[0].Arguments))
		case 2:
			var definition dispatch.Definition
			for _, item := range req.Input {
				var m struct{ Content []struct{ Text string } }
				if json.Unmarshal(item, &m) == nil {
					for _, part := range m.Content {
						var task struct {
							Work dispatch.Definition `json:"worker_definition"`
						}
						if json.Unmarshal([]byte(part.Text), &task) == nil && task.Work.Repository != "" {
							definition = task.Work
						}
					}
				}
			}
			if definition.Repository == "" {
				t.Error("published task definition missing")
			}
			emitPersonaCall(w, "permitted-worker", "dispatch_work", definition)
		default:
			emitResponse(w, "Task admitted.", true)
		}
	})))
	defer provider.Close()
	var revoked atomic.Bool
	issuer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/introspect" {
			http.NotFound(w, r)
			return
		}
		u, p, ok := r.BasicAuth()
		if !ok || u != "core" || p != "fixture-secret" {
			http.Error(w, "denied", 401)
			return
		}
		r.ParseForm()
		token := r.Form.Get("token")
		subject := "one"
		if token == "other" {
			subject = "two"
		}
		expires := time.Now().Add(time.Hour).Unix()
		if token == "expired" {
			expires = 1
		}
		json.NewEncoder(w).Encode(map[string]any{"active": !revoked.Load() && token != "invalid", "iss": "https://" + r.Host, "aud": "a2a", "exp": expires, "sub": subject, "client_id": "caller", "scope": "tasks", "token_type": "Bearer"})
	}))
	defer issuer.Close()
	certPath, keyPath := filepath.Join(fixture, "tls.pem"), filepath.Join(fixture, "tls.key")
	cert := issuer.TLS.Certificates[0]
	key, err := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]}), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}), 0600); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, buildLina(t))
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	h.ctx = ctx
	bundle := makeInstallBundle(t, h.root, executable, buildCommand(t, "lina-node"))
	h, _ = installConversationBundle(t, h, bundle, provider.URL)
	load := newWorkerLoad(t, fixture)
	workerMode := mode
	if mode == "verified" {
		workerMode = "overlap"
	}
	if mode == "unverified" {
		workerMode = "success"
	}
	if mode == "revoked-running" {
		workerMode = "uncertain-cancel"
	}
	start := func() *process {
		p := h.command("-test.run=^TestA2ACoreProcess$")
		p.command.Env = append(p.command.Env, "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "LINA_A2A_TEST_ROOT="+h.root, "LINA_A2A_TEST_SOCKET="+h.socket, "LINA_A2A_TEST_CA="+certPath, "LINA_WORKER_FIXTURE="+fixture, "LINA_WORKER_CASE="+workerMode, "LINA_RESPONSES_BASE_URL="+provider.URL+"/v1", "LINA_RESPONSES_MODEL=fixture-model", "LINA_RESPONSES_API_KEY=synthetic-key")
		load.wrap(t, p)
		p.start(t)
		h.connectWithin(p, time.Minute).Close()
		waitCoreReady(t, h)
		return p
	}
	bootstrap := start()
	client, _ := conversationClient(t, h)
	person := client.Welcome.PersonID
	client.Close()
	h.stopCore(bootstrap)
	ids := seedMethodologyAgents(t, h, person, "dev")
	vault, err := secrets.OpenFiles(h.root)
	if err != nil {
		t.Fatal(err)
	}
	if err = vault.Put(secrets.Key{Person: person, Service: a2a.CredentialService(a2a.OAuth{Issuer: issuer.URL, ClientID: "core"}), Name: "issuer"}, []byte(base64.StdEncoding.EncodeToString([]byte("core:fixture-secret")))); err != nil {
		t.Fatal(err)
	}
	vault.Close()
	socket := filepath.Join(fixture, "tailscaled.sock")
	daemon, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	localAPI := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/localapi/v0/status" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `{"BackendState":"Running","TailscaleIPs":["100.100.0.1"],"Self":{"ID":"self","UserID":7}}`)
	})}
	daemonDone := make(chan struct{})
	go func() { defer close(daemonDone); localAPI.Serve(daemon) }()
	defer func() { localAPI.Close(); <-daemonDone }()
	cfg := a2a.Config{Enabled: true, Address: "100.100.0.1:9443", TailscaleSocket: socket, Certificate: certPath, PrivateKey: keyPath, OAuth: a2a.OAuth{Issuer: issuer.URL, Audience: "a2a", IntrospectionURL: issuer.URL + "/introspect", ClientID: "core", SecretName: "issuer", Scopes: []string{"tasks"}}, Publications: []a2a.Publication{{ID: "published", Agent: ids["dev"], Conversation: "main:" + ids["dev"], Name: "Published", Description: "Approved task", URL: "https://127.0.0.1/a2a/published", Definitions: []a2a.Definition{def}, Bindings: []a2a.Binding{{Subject: "one", Client: "caller", Definitions: []string{def.ID}, Expires: time.Now().Add(time.Hour).Unix(), MaxActive: 2}, {Subject: "two", Client: "caller", Definitions: []string{def.ID}, Expires: time.Now().Add(time.Hour).Unix(), MaxActive: 2}}}}}
	configPath := filepath.Join(h.root, "system/core/a2a.json")
	writeConfig := func() {
		t.Helper()
		raw, _ := json.Marshal(cfg)
		if err = os.WriteFile(configPath, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeConfig()
	core := start()
	t.Cleanup(func() {
		if t.Failed() {
			logWorkerDispatchState(t, h, fixture)
			logA2ATaskState(t, h)
			t.Logf("provider task calls=%d", calls.Load())
		}
	})
	endpointRaw, err := os.ReadFile(filepath.Join(h.root, "endpoint"))
	if err != nil {
		t.Fatal(err)
	}
	endpoint := string(endpointRaw)
	cfg.Publications[0].URL = endpoint
	writeConfig()
	httpClient := issuer.Client()
	httpClient.Timeout = 15 * time.Second
	request := func(token, method string, params any) (int, json.RawMessage) {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
		req, err := http.NewRequestWithContext(h.ctx, "POST", endpoint, bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("A2A-Version", "1.0")
		res, err := httpClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		body, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		return res.StatusCode, body
	}
	params := map[string]any{"message": map[string]any{"messageId": "send-once", "role": "ROLE_USER", "parts": []any{map[string]any{"data": map[string]string{"task": scenario.Task, "input": scenario.Input}}}}, "configuration": map[string]any{"returnImmediately": true}}
	for _, token := range []string{"invalid", "expired"} {
		if status, body := request(token, "SendMessage", params); status != 401 {
			t.Fatalf("%s admitted: %d %s", token, status, body)
		}
	}
	var ownerApproval *protocol.ApprovalRequest
	var ownerClient *clientapi.Client
	if mode == "denied" {
		ownerClient, err = clientapi.DialWithCapabilities(h.ctx, h.socket, "test", []protocol.Capability{{Name: "waiting", Version: "1.0"}})
		if err != nil {
			t.Fatal(err)
		}
		defer ownerClient.Close()
		topic := "waiting"
		if _, err = ownerClient.Subscribe(h.ctx, protocol.Subscribe{Topic: &topic}); err != nil {
			t.Fatal(err)
		}
		if _, err = ownerClient.Receive(h.ctx); err != nil {
			t.Fatal(err)
		}
	}
	status, body := 0, json.RawMessage(nil)
	if mode == "verified" {
		raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "SendStreamingMessage", "params": params})
		req, err := http.NewRequestWithContext(h.ctx, "POST", endpoint, bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer valid")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("A2A-Version", "1.0")
		resp, err := httpClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		status = resp.StatusCode
		line, err := bufio.NewReader(resp.Body).ReadString('\n')
		resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		body = json.RawMessage(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
	} else {
		status, body = request("valid", "SendMessage", params)
	}
	var admitted struct{ Result struct{ Task a2a.Task } }
	if status != 200 || json.Unmarshal(body, &admitted) != nil || admitted.Result.Task.ID == "" {
		t.Fatalf("send: %d %s", status, body)
	}
	id := admitted.Result.Task.ID
	if mode == "verified" && a2a.Terminal(admitted.Result.Task.Status.State) {
		t.Fatal("stream closed after work was already terminal")
	}
	_, body = request("valid", "SendMessage", params)
	var duplicate struct{ Result struct{ Task a2a.Task } }
	json.Unmarshal(body, &duplicate)
	if duplicate.Result.Task.ID != id {
		t.Fatal("duplicate created work", string(body))
	}
	for _, method := range []string{"GetTask", "SubscribeToTask", "CancelTask"} {
		_, body = request("other", method, map[string]string{"id": id})
		if !bytes.Contains(body, []byte(`"code":-32001`)) {
			t.Fatalf("cross caller %s: %s", method, body)
		}
	}
	_, body = request("other", "ListTasks", map[string]any{})
	if bytes.Contains(body, []byte(id)) || !bytes.Contains(body, []byte(`"totalSize":0`)) {
		t.Fatalf("cross caller list: %s", body)
	}

	if mode == "verified" {
		if err = os.WriteFile(filepath.Join(fixture, "release-first"), []byte("1"), 0600); err != nil {
			t.Fatal(err)
		}
	}

	if mode == "denied" {
		receiveCtx, stopReceive := context.WithCancel(h.ctx)
		type received struct {
			frame protocol.Envelope
			err   error
		}
		frames := make(chan received, 1)
		receiveDone := make(chan struct{})
		go func() {
			defer close(receiveDone)
			for {
				frame, err := ownerClient.Receive(receiveCtx)
				select {
				case frames <- received{frame, err}:
				case <-receiveCtx.Done():
					return
				}
				if err != nil {
					return
				}
			}
		}()
		defer func() { stopReceive(); <-receiveDone }()
		poll := time.NewTicker(50 * time.Millisecond)
		defer poll.Stop()
		for ownerApproval == nil {
			select {
			case next := <-frames:
				if next.err != nil {
					t.Fatal(next.err)
				}
				if approval, ok := next.frame.Payload.(protocol.ApprovalRequest); ok {
					ownerApproval = &approval
				}
			case <-poll.C:
				_, body = request("valid", "GetTask", map[string]string{"id": id})
				var task struct{ Result a2a.Task }
				json.Unmarshal(body, &task)
				if a2a.Terminal(task.Result.Status.State) {
					t.Fatalf("task stopped before an approval: %s", body)
				}
			case <-h.ctx.Done():
				t.Fatal(h.ctx.Err())
			}
		}
		stopReceive()
		<-receiveDone

		if ownerApproval.Card == nil || ownerApproval.ConversationID == nil || *ownerApproval.ConversationID != cfg.Publications[0].Conversation {
			t.Fatal("approval lost owner conversation")
		}
		_, body = request("valid", "AnswerApproval", map[string]any{"id": ownerApproval.Card.ID, "choice": "once"})
		if !bytes.Contains(body, []byte(`"code":-32601`)) {
			t.Fatal("caller answered owner approval", string(body))
		}
		if _, err = ownerClient.AnswerApproval(h.ctx, protocol.ApprovalAnswer{CardID: &ownerApproval.Card.ID, Revision: &ownerApproval.Card.Revision, Choice: protocol.ApprovalChoiceDeny}); err != nil {
			t.Fatal(err)
		}
	}
	if mode == "uncertain-cancel" || mode == "revoked-running" {
		poll := time.NewTicker(20 * time.Millisecond)
		defer poll.Stop()
		for {
			if _, err = os.Stat(filepath.Join(fixture, "effect-observed")); err == nil {
				break
			}
			select {
			case <-h.ctx.Done():
				t.Fatal("worker never reached its effect")
			case <-poll.C:
			}
		}
		if mode == "revoked-running" {
			originalBindings := cfg.Publications[0].Bindings
			cfg.Publications[0].Bindings = cfg.Publications[0].Bindings[1:]
			writeConfig()
			if status, _ := request("valid", "GetTask", map[string]string{"id": id}); status != 403 {
				t.Fatal("revoked caller could read running task")
			}
			for {
				records, err := workerDispatchRecords(h.ctx, h)
				if err != nil {
					t.Fatal(err)
				}
				if len(records) == 1 && records[0].Canceled && records[0].State != "running" {
					break
				}
				select {
				case <-h.ctx.Done():
					t.Fatal("revoked authority did not stop worker")
				case <-poll.C:
				}
			}
			cfg.Publications[0].Bindings = originalBindings
			writeConfig()
		} else {
			_, body = request("valid", "CancelTask", map[string]string{"id": id})
			if bytes.Contains(body, []byte(a2a.Completed)) {
				t.Fatal("cancel acknowledgment completed work")
			}
		}
		for {
			_, body = request("valid", "GetTask", map[string]string{"id": id})
			if bytes.Contains(body, []byte(a2a.Unspecified)) {
				break
			}
			select {
			case <-h.ctx.Done():
				t.Fatal("uncertain cancel lost its state", string(body))
			case <-poll.C:
			}
		}
		h.stopCore(core)
		core = start()
		defer h.stopCore(core)
		endpointRaw, err = os.ReadFile(filepath.Join(h.root, "endpoint"))
		if err != nil {
			t.Fatal(err)
		}
		endpoint = string(endpointRaw)
		_, body = request("valid", "GetTask", map[string]string{"id": id})
		if !bytes.Contains(body, []byte(a2a.Unspecified)) || bytes.Contains(body, []byte(a2a.Completed)) {
			t.Fatal("restart invented cancellation evidence", string(body))
		}
		launches, err := workerLaunches(fixture)
		if err != nil || launches != "worker\n" {
			t.Fatal("unknown work restarted", launches, err)
		}
		return
	}
	var completed a2a.Task
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		_, body = request("valid", "GetTask", map[string]string{"id": id})
		var got struct{ Result a2a.Task }
		json.Unmarshal(body, &got)
		if a2a.Terminal(got.Result.Status.State) {
			completed = got.Result
			break
		}
		select {
		case <-h.ctx.Done():
			t.Fatalf("task did not finish: %s", body)
		case <-tick.C:
		}
	}
	if mode == "unverified" || mode == "denied" {
		if completed.Status.State == a2a.Completed || len(completed.Artifacts) != 0 {
			t.Fatalf("unverified completion: %+v", completed)
		}
		launches, err := workerLaunches(fixture)
		want := ""
		if mode == "denied" {
			want = "worker\n"
			if _, err := os.Stat(filepath.Join(fixture, "owner-denied")); err != nil {
				t.Fatal("owner denial did not reach worker", err)
			}
		}
		if err != nil || launches != want {
			t.Fatal("unexpected work", launches, err)
		}
		h.stopCore(core)
		return
	}
	if completed.Status.State != a2a.Completed || len(completed.Artifacts) != 1 || string(completed.Artifacts[0].Parts[0].Raw) != scenario.Artifact {
		t.Fatalf("unverified/missing artifact: %+v", completed)
	}
	for _, token := range []string{"valid", "other"} {
		req, _ := http.NewRequestWithContext(h.ctx, "GET", endpoint+"/artifacts/"+id+"/result", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("A2A-Version", "1.0")
		response, err := httpClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if token == "valid" {
			if response.StatusCode != 200 || string(data) != scenario.Artifact {
				t.Fatal("artifact download", response.StatusCode, string(data))
			}
		} else if response.StatusCode != 404 || bytes.Contains(data, []byte(scenario.Artifact)) {
			t.Fatal("artifact leaked", response.StatusCode, string(data))
		}
	}
	// Wait for the independently durable owner delivery, then read it through the
	// existing conversation protocol after a full Core restart.
	for {
		db, err := canon.OpenReader(h.ctx, filepath.Join(h.root, "canon/canon.sqlite"))
		if err != nil {
			t.Fatal(err)
		}
		var delivered string
		err = db.Read(h.ctx, func(tx *canon.Tx) error {
			return tx.QueryRowContext(h.ctx, "SELECT json_extract(record,'$.delivered') FROM a2a_tasks WHERE id=?", id).Scan(&delivered)
		})
		db.Close(context.Background())
		if err == nil && delivered != "" {
			break
		}
		select {
		case <-h.ctx.Done():
			t.Fatal("owner result was not delivered", err)
		case <-tick.C:
		}
	}
	assertA2ACommittedTool(t, h, id)
	workdir, err := os.ReadFile(filepath.Join(fixture, "workdir"))
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command("git", "-C", string(workdir), "show", "HEAD:result.txt")
	command.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + fixture, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
	committed, err := command.Output()
	if err != nil || string(committed) != scenario.Artifact {
		t.Fatalf("worker commit mismatch: %q %v", committed, err)
	}
	launches, err := workerLaunches(fixture)
	if err != nil || launches != "worker\n" {
		t.Fatalf("worker launches: %q %v", launches, err)
	}
	h.stopCore(core)
	core = start()
	defer h.stopCore(core)
	endpointRaw, err = os.ReadFile(filepath.Join(h.root, "endpoint"))
	if err != nil {
		t.Fatal(err)
	}
	endpoint = string(endpointRaw)
	// The public URL remains fixed across restart; the test routes the new
	// physical loopback socket without changing the publication authority.
	_, body = request("valid", "GetTask", map[string]string{"id": id})
	if !bytes.Contains(body, []byte(a2a.Completed)) {
		t.Fatalf("restart lost result: %s", body)
	}
	c, history := conversationClient(t, h)
	defer c.Close()
	topic := "conversation:" + history
	if _, err := c.Subscribe(h.ctx, protocol.Subscribe{Topic: &topic}); err != nil {
		t.Fatal(err)
	}
	snap, err := c.Receive(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(snap.Payload)
	if !bytes.Contains(encoded, []byte(strings.TrimSpace(scenario.Artifact))) {
		t.Fatalf("owner conversation lost result: %s", encoded)
	}
	launches, err = workerLaunches(fixture)
	if err != nil || launches != "worker\n" {
		t.Fatalf("restart repeated work: %q %v", launches, err)
	}
	assertA2ACommittedTool(t, h, id)
	revoked.Store(true)
	if status, _ := request("valid", "GetTask", map[string]string{"id": id}); status != 401 {
		t.Fatal("revoked access token still authorized")
	}
	revoked.Store(false)
	cfg.Publications[0].Bindings[0].Expires = 1
	writeConfig()
	if status, _ := request("valid", "GetTask", map[string]string{"id": id}); status != 403 {
		t.Fatal("revoked binding still authorized")
	}
}

func assertA2ACommittedTool(t *testing.T, h *harness, task string) {
	t.Helper()
	db, err := canon.OpenReader(h.ctx, filepath.Join(h.root, "canon/canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	var count int
	var file string
	if err = db.Read(h.ctx, func(tx *canon.Tx) error {
		if err := tx.QueryRowContext(h.ctx, "SELECT count(*) FROM turn_calls WHERE turn_id=? AND json_extract(call,'$.Name')='write_record' AND json_extract(result,'$.code')='ok'", "turn:a2a-input:"+task).Scan(&count); err != nil {
			return err
		}
		return tx.QueryRowContext(h.ctx, "SELECT path FROM records_index WHERE id=(SELECT json_extract(result,'$.output.id') FROM turn_calls WHERE turn_id=? AND json_extract(call,'$.Name')='write_record' AND json_extract(result,'$.code')='ok')", "turn:a2a-input:"+task).Scan(&file)
	}); err != nil || count != 1 {
		t.Fatalf("Core tool did not commit exactly once: count=%d err=%v", count, err)
	}
	root := filepath.Join(h.root, "versions/records")
	data, err := os.ReadFile(filepath.Join(root, file))
	if err != nil || !bytes.Contains(data, []byte("The permitted Core tool ran.")) {
		t.Fatal("Core tool's canonical record missing", err)
	}
	cmd := exec.Command("git", "-C", root, "show", "HEAD:"+file)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + h.root, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
	committed, err := cmd.Output()
	if err != nil || !bytes.Equal(committed, data) {
		t.Fatal("Core tool record is not committed", err)
	}
	var results int
	if err = db.Read(h.ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(h.ctx, "SELECT count(*) FROM result_heads WHERE id=(SELECT json_extract(record,'$.delivered') FROM a2a_tasks WHERE id=?)", task).Scan(&results)
	}); err != nil || results != 1 {
		t.Fatalf("owner result count=%d err=%v", results, err)
	}
}

func logA2ATaskState(t *testing.T, h *harness) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db, err := canon.OpenReader(ctx, filepath.Join(h.root, "canon/canon.sqlite"))
	if err != nil {
		t.Log(err)
		return
	}
	defer db.Close(context.Background())
	err = db.Read(ctx, func(tx *canon.Tx) error {
		for _, query := range []string{
			"SELECT json_object('phase',phase,'state',json_extract(record,'$.task.status.state'),'input',json_extract(record,'$.input_event'),'calls',json_extract(record,'$.tool_calls')) FROM a2a_tasks",
			"SELECT json_object('turn',id,'state',state,'prepared',prepared) FROM turn_runs",
			"SELECT json_object('call',call_id,'name',json_extract(call,'$.Name'),'result',json(result)) FROM turn_calls",
			"SELECT CAST(outcome AS TEXT) FROM turn_outcome",
		} {
			rows, err := tx.QueryContext(ctx, query)
			if err != nil {
				return err
			}
			for rows.Next() {
				var value string
				if err = rows.Scan(&value); err != nil {
					rows.Close()
					return err
				}
				if len(value) > 1200 {
					value = value[:1200]
				}
				t.Log(value)
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Log(err)
	}
}
