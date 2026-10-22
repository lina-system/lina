// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/update"
)

func TestM2UpdatePrepare(t *testing.T) {
	binary := buildLina(t)
	h := newHarness(t, binary)
	core := h.startCore()
	h.connect(core).Close()
	h.stopCore(core)
	if err := os.Mkdir(filepath.Join(h.root, "system/updates"), 0700); err != nil {
		t.Fatal(err)
	}
	// Preparation must work alongside the actual running Core, whose held
	// lock would reject a second ordinary startup.
	core = h.startCore()
	live := restartClient(h, core)
	bundle := makeInstallBundle(t, t.TempDir(), binary, buildCommand(t, "lina-node"))
	archive, digest := packUpdate(t, bundle)
	before := doctorState(t, h.root)
	command := func(source, sha string, code int) *process {
		p := h.command("update", "--prepare", "--state-root", h.root, "--archive", source, "--sha256", sha)
		p.start(t)
		p.wait(t, code)
		return p
	}
	command(archive, strings.Repeat("0", 64), 78)
	p := command(archive, digest, 0)
	var prepared update.Prepared
	if err := json.Unmarshal(p.stdout.Bytes(), &prepared); err != nil {
		t.Fatal(err)
	}
	if prepared.SHA256 != digest || prepared.Preflight.SchemaRelation != "equal" || prepared.Preflight.PeerRows < 1 || prepared.Gate.Candidate != update.Pass || prepared.Gate.Schema != update.Pass || prepared.Gate.Peers != update.Pass || prepared.Gate.Space != update.Pass || prepared.Gate.Ready() {
		t.Fatalf("incomplete or prematurely ready preparation: %+v", prepared)
	}
	if prepared.Gate.Idle != update.Unknown || prepared.Gate.Generation != update.Unknown {
		t.Fatal("preparation invented idle/backup evidence")
	}
	receipt, err := os.ReadFile(filepath.Join(prepared.Directory, "prepared.json"))
	if err != nil {
		t.Fatal(err)
	}
	var persisted update.Prepared
	if err := json.Unmarshal(receipt, &persisted); err != nil || !reflect.DeepEqual(prepared, persisted) {
		t.Fatal("missing preparation receipt", err)
	}
	// Repeat preparation must preserve the existing candidate, not overwrite it.
	command(archive, digest, 78)
	if got, err := os.ReadFile(filepath.Join(prepared.Directory, "prepared.json")); err != nil || string(got) != string(receipt) {
		t.Fatal("existing version changed", err)
	}
	after := doctorState(t, h.root)
	for name, file := range before {
		if name == "system/updates" {
			continue
		}
		if got, ok := after[name]; !ok || got != file {
			t.Fatalf("preparation changed active state: %s", name)
		}
	}
	live.conn.Close()
	h.stopCore(core)
	// The verified candidate is a real Core: its preflight success did not mint
	// authority, but it can subsequently serve the same installed store.
	candidate := *h
	candidate.binary = filepath.Join(prepared.Directory, "lina")
	candidateCore := candidate.startCore()
	client := candidate.client(candidateCore)
	candidate.inputRefused(client)
	client.conn.Close()
	candidate.stopCore(candidateCore)

	// A digest-valid composition can still be refused by the separate Core's
	// compatibility check. It must not be published as a prepared candidate.
	metadata := filepath.Join(bundle, "manifest.json")
	original, err := os.ReadFile(metadata)
	if err != nil {
		t.Fatal(err)
	}
	var unsupported map[string]any
	if err := json.Unmarshal(original, &unsupported); err != nil {
		t.Fatal(err)
	}
	unsupported["supportedCombinations"] = []any{}
	data, err := json.Marshal(unsupported)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(metadata, data, 0600); err != nil {
		t.Fatal(err)
	}
	refused, refusedDigest := packUpdate(t, bundle)
	command(refused, refusedDigest, 78)
	if _, err := os.Lstat(filepath.Join(h.root, "system/updates", refusedDigest)); !os.IsNotExist(err) {
		t.Fatal("preflight refusal retained a candidate", err)
	}
	if err := os.WriteFile(metadata, original, 0600); err != nil {
		t.Fatal(err)
	}

	// Retain the archive digest while invalidating a manifest's artifact digest.
	if err := os.WriteFile(filepath.Join(bundle, "lina-node"), []byte("changed artifact"), 0700); err != nil {
		t.Fatal(err)
	}
	invalid, invalidDigest := packUpdate(t, bundle)
	command(invalid, invalidDigest, 78)
	if _, err := os.Lstat(filepath.Join(h.root, "system/updates", invalidDigest)); !os.IsNotExist(err) {
		t.Fatal("failed candidate retained", err)
	}
	entries, err := os.ReadDir(filepath.Join(h.root, "system/updates"))
	if err != nil || len(entries) != 1 || entries[0].Name() != digest {
		t.Fatalf("temporary preparation leaked: %v %v", entries, err)
	}
}

func packUpdate(t *testing.T, bundle string) (string, string) {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), "update-*.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.New()
	compressed := gzip.NewWriter(io.MultiWriter(file, hash))
	writer := tar.NewWriter(compressed)
	entries, err := os.ReadDir(bundle)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			t.Fatal(err)
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			t.Fatal(err)
		}
		if err := writer.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		input, err := os.Open(filepath.Join(bundle, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		_, copyErr := io.Copy(writer, input)
		input.Close()
		if copyErr != nil {
			t.Fatal(copyErr)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := compressed.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return file.Name(), fmt.Sprintf("%x", hash.Sum(nil))
}
