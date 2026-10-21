// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"
)

func main() {
	root := os.Args[1]
	failed := false
	fail := func(what string) { fmt.Fprintln(os.Stderr, what); failed = true }
	defer func() {
		if failed {
			os.Exit(1)
		}
	}()
	for _, p := range []string{"canon/canary", "secrets/canary", "work/alias/canary"} {
		if b, err := os.ReadFile(filepath.Join(root, p)); err == nil && len(b) > 0 {
			fail("protected read: " + p)
		}
	}
	if conn, err := net.DialTimeout("unix", filepath.Join(root, "client.sock"), time.Second); err == nil {
		conn.Close()
		fail("client socket reachable")
	}
	for _, name := range []string{"client-alias.sock"} {
		if conn, err := net.DialTimeout("unix", filepath.Join(root, name), time.Second); err == nil {
			conn.Close()
			fail("client socket alias reachable")
		}
	}
	for _, p := range []string{"outside", "work/nested/.git/config"} {
		if err := os.WriteFile(filepath.Join(root, p), []byte("unsafe"), 0600); err == nil {
			fail("protected write: " + p)
		}
	}
	if err := os.Rename(filepath.Join(root, "work/nested"), filepath.Join(root, "work/moved")); err == nil {
		fail("Git parent could be replaced")
	}
	if err := os.WriteFile(filepath.Join(root, "work/allowed"), []byte("ok"), 0600); err != nil {
		fail("workspace not writable")
	}
	if os.Getenv("OPENAI_API_KEY") != "" || os.Getenv("GH_TOKEN") != "user-setup" {
		fail("environment isolation")
	}
	conn, err := net.DialTimeout("tcp", os.Args[2], time.Second)
	if conn != nil {
		conn.Close()
	}
	if (err == nil) != (os.Args[3] == "true") {
		fail("network authorization not enforced")
	}
	if err := platformCanary(); err != nil {
		fail(err.Error())
	}
}
