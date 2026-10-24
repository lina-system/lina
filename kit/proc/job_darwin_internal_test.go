// SPDX-License-Identifier: Apache-2.0

//go:build darwin

package proc

import (
	"bytes"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestJobDefinitionNamesOneMoreNestingLevel guards the recursion defence: a job
// definition carries the depth of the process that wrote it, plus one, and the
// helper refuses to run above the first level.
func TestJobDefinitionNamesOneMoreNestingLevel(t *testing.T) {
	marker := "<key>" + JobDepthVariable + "</key><string>"
	definition := string(jobPlist("com.lina.proc.test", "/bin/lina", "/tmp/request", "/tmp", false))
	if !strings.Contains(definition, marker+"1</string>") {
		t.Fatalf("job definition does not start at depth 1")
	}
	t.Setenv(JobDepthVariable, "1")
	definition = string(jobPlist("com.lina.proc.test", "/bin/lina", "/tmp/request", "/tmp", false))
	if !strings.Contains(definition, marker+"2</string>") {
		t.Fatalf("a job written inside a job is not deeper")
	}
}

// TestSameWriterOnlySharesComparableWriters proves the shared-writer lock is
// taken only when the two destinations really are one writer: an uncomparable
// writer must not panic the comparison, and different writers must not share a
// lock.
func TestSameWriterOnlySharesComparableWriters(t *testing.T) {
	var buffer bytes.Buffer
	if sameWriter(nil, nil) || sameWriter(&buffer, nil) {
		t.Fatal("a nil writer is never the other writer")
	}
	if !sameWriter(&buffer, &buffer) {
		t.Fatal("one writer supplied twice is not shared")
	}
	var other bytes.Buffer
	if sameWriter(&buffer, &other) {
		t.Fatal("different writers share a lock")
	}
	uncomparable := func() io.Writer { return sliceWriter{[]*bytes.Buffer{&buffer}} }
	if sameWriter(uncomparable(), uncomparable()) {
		t.Fatal("an uncomparable writer was treated as shared")
	}
}

// sliceWriter is an io.Writer whose dynamic type cannot be compared, like the
// writers the standard library itself uses.
type sliceWriter struct{ buffers []*bytes.Buffer }

func (w sliceWriter) Write(p []byte) (int, error) { return w.buffers[0].Write(p) }

// TestOwnerResolvesTheJobDirectory keeps the job's private directory out of the
// command's reach: an unset or relative directory is resolved against the
// owner's own, and an absolute one is left alone.
func TestOwnerResolvesTheJobDirectory(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if got, err := ownerDirectory(""); err != nil || got != wd {
		t.Fatalf("unset directory = %q, %v; want %q", got, err, wd)
	}
	if got, err := ownerDirectory("relative/dir"); err != nil || got != strings.TrimSuffix(wd, "/")+"/relative/dir" {
		t.Fatalf("relative directory = %q, %v", got, err)
	}
	// A symlink followed by ".." is resolved by the kernel, not lexically here:
	// "link/.." must arrive uncleaned so it selects the symlink's parent. The
	// expectation is built by concatenation because filepath.Join would clean
	// the ".." away and make this check pass for the wrong implementation.
	want := wd + string(filepath.Separator) + "link" + string(filepath.Separator) + ".."
	if got, err := ownerDirectory("link/.."); err != nil || got != want {
		t.Fatalf("relative directory with a symlink = %q, %v; want %q", got, err, want)
	}
	if got, err := ownerDirectory("/tmp"); err != nil || got != "/tmp" {
		t.Fatalf("absolute directory = %q, %v", got, err)
	}
	if got := ownerEnvironment(nil); got == nil || len(got) != 0 {
		t.Fatalf("nil environment = %#v, want an empty list", got)
	}
	if got := ownerEnvironment([]string{"A=1"}); len(got) != 1 || got[0] != "A=1" {
		t.Fatalf("explicit environment = %#v", got)
	}
}

// TestDrainJoinsFileStdinWithoutClosingIt is the launchd owner's half of the
// file relay. After drain, a pipe whose writer is still open is not copied to
// the finished command, and the caller can read what it writes next.
func TestDrainJoinsFileStdinWithoutClosingIt(t *testing.T) {
	process, reader, writer, client := startFileInput(t)
	deliverInput(t, writer, client, "one")
	if err := process.drain(); err != nil {
		t.Fatal(err)
	}
	assertInputStaysWithCaller(t, reader, writer, client)
}

// TestReleaseJoinsFileStdinWithoutClosingIt covers a start that fails after
// the relay is running. Run returns through release without calling drain.
func TestReleaseJoinsFileStdinWithoutClosingIt(t *testing.T) {
	process, reader, writer, client := startFileInput(t)
	deliverInput(t, writer, client, "one")
	process.release()
	assertInputStaysWithCaller(t, reader, writer, client)
}

func startFileInput(t *testing.T) (*jobProcess, *os.File, *os.File, *net.UnixConn) {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reader.Close(); writer.Close() })
	dir, err := os.MkdirTemp("/tmp", "lina-stdin-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(dir, "in.sock"), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	accepted := make(chan *net.UnixConn, 1)
	go func() {
		conn, err := listener.AcceptUnix()
		if err != nil {
			return
		}
		accepted <- conn
	}()
	client, err := net.DialUnix("unix", nil, listener.Addr().(*net.UnixAddr))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	var server *net.UnixConn
	select {
	case server = <-accepted:
	case <-time.After(2 * time.Second):
		t.Fatal("input socket was not accepted")
	}
	t.Cleanup(func() { server.Close() })
	process := &jobProcess{
		dir:     dir,
		stdin:   reader,
		streams: map[string]*jobStream{"in": {conn: server, pump: make(chan error, 1)}},
		stopped: make(chan struct{}),
	}
	process.relayInput(server, process.streams["in"])
	return process, reader, writer, client
}

func deliverInput(t *testing.T, writer *os.File, client *net.UnixConn, text string) {
	t.Helper()
	if _, err := writer.Write([]byte(text)); err != nil {
		t.Fatal(err)
	}
	if err := client.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, len(text))
	n, err := client.Read(buf)
	if err != nil || string(buf[:n]) != text {
		t.Fatalf("command read %q, %v", buf[:n], err)
	}
}

func assertInputStaysWithCaller(t *testing.T, reader, writer *os.File, client *net.UnixConn) {
	t.Helper()
	if _, err := writer.Write([]byte("two")); err != nil {
		t.Fatal(err)
	}
	if err := client.SetReadDeadline(time.Now().Add(200 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if n, err := client.Read(buf); err == nil {
		t.Fatalf("finished relay delivered %q", buf[:n])
	}
	if err := reader.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	n, err := reader.Read(buf)
	if err != nil || string(buf[:n]) != "two" {
		t.Fatalf("caller read %q, %v", buf[:n], err)
	}
}
