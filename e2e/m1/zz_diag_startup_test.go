// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Temporary diagnostic: measure installed Core startup latency and the cost of
// each boot boundary using an instrumented binary. Not for commit.
func TestZZDiagCoreStartupLatency(t *testing.T) {
	out := os.Getenv("LINA_DIAG_OUT")
	binary := os.Getenv("LINA_DIAG_BINARY")
	if binary == "" {
		t.Fatal("LINA_DIAG_BINARY required")
	}
	h := newHarness(t, binary)
	bundle := makeInstallBundle(t, h.root, binary, buildCommand(t, "lina-node"))
	h, _ = installConversationBundle(t, h, bundle, "")
	h.binary = binary
	log := func(format string, args ...any) {
		line := fmt.Sprintf(format, args...)
		t.Log(line)
		if out != "" {
			f, err := os.OpenFile(filepath.Join(out), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
			if err == nil {
				fmt.Fprintln(f, line)
				f.Close()
			}
		}
	}
	for i := range 3 {
		begin := time.Now()
		p := h.command("core", "--state-root", h.root, "--socket", h.socket)
		p.command.Env = append(p.command.Env, "LINA_DIAG_STEPS=1")
		if traceFile := os.Getenv("LINA_DIAG_STRACE"); traceFile != "" {
			args := append([]string{"-f", "-tt", "-e", "trace=bind,listen,connect,unlink,unlinkat,rename,renameat,renameat2,mkdir,mkdirat", "-o", traceFile + "." + fmt.Sprint(i)}, p.command.Args...)
			p.command.Args = args
			p.command.Path = "/usr/bin/strace"
		}
		p.start(t)
		attempts := 0
		var connErr []string
		var conn net.Conn
		var trace []string
		lastTrace := time.Duration(0)
		for {
			attempts++
			c, err := net.Dial("unix", h.socket)
			if err == nil {
				conn = c
				break
			}
			connErr = append(connErr, err.Error())
			if elapsed := time.Since(begin); elapsed-lastTrace > 200*time.Millisecond {
				lastTrace = elapsed
				entries, dirErr := os.ReadDir(filepath.Dir(h.socket))
				names := make([]string, 0, len(entries))
				for _, e := range entries {
					names = append(names, e.Name())
				}
				trace = append(trace, fmt.Sprintf("%s err=%v dir=%v dirErr=%v stderr=%q", elapsed.Round(time.Millisecond), err, names, dirErr, p.stderr.String()))
			}
			if time.Since(begin) > 5*time.Minute {
				break
			}
			time.Sleep(time.Millisecond)
		}
		listen := time.Since(begin)
		if conn != nil {
			conn.Close()
		}
		h.stopCore(p)
		log("%s boot %d listen=%s attempts=%d firsterr=%v\n%s\n%s", os.Getenv("LINA_DIAG_TAG"), i, listen.Round(time.Millisecond), attempts, connErr[:min(3, len(connErr))], strings.Join(trace, "\n"), p.stderr.String())
	}
}
