// SPDX-License-Identifier: Apache-2.0

package ghclient

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestReadPlainChildResponses(t *testing.T) {
	for _, tty := range []string{"", "0", "120", "80%"} {
		t.Run("tty="+tty, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			config := filepath.Join(dir, "hosts.yml")
			const login = "github.com:\n    user: fixture-user\n    oauth_token: fixture-stored-token\n"
			if err := os.WriteFile(config, []byte(login), 0600); err != nil {
				t.Fatal(err)
			}
			// This executable models gh's documented terminal/color behavior at
			// the real proc boundary; it never contacts GitHub or reads user files.
			script := `#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
set -eu
printf '%s\n' "${GH_FORCE_TTY-unset}" "${NO_COLOR-}" "${CLICOLOR-}" "${CLICOLOR_FORCE-unset}" "${FORCE_COLOR-unset}" "${TERM-}" "${HOME-}" "${GH_CONFIG_DIR-}" "${GH_TOKEN-}" "${GITHUB_TOKEN-}" "${GH_HOST-}" "${PATH-}" "${LINA_RESPONSES_API_KEY-}" > "$GH_CONFIG_DIR/child-env"
if [ -z "${NO_COLOR-}" ] && [ "${CLICOLOR-}" != 0 ]; then
 if [ -n "${GH_FORCE_TTY-}" ] || [ "${CLICOLOR_FORCE-0}" != 0 ]; then
  printf '\033[32m'
 fi
fi
case "$*" in
`
			replies := bundle("OPEN", "COMPLETED", "SUCCESS", sha)
			for i, pattern := range []string{"*pulls/140*", "*graphql*", "*check-runs*", "*/status*"} {
				r := replies[i]
				response := fmt.Sprintf("HTTP/2.0 %d OK\r\n%s\r\n%s", r.code, r.headers, r.body)
				script += pattern + ") printf '%s' '" + strings.ReplaceAll(response, "'", "'\"'\"'") + "' ;;\n"
			}
			script += "*) exit 90 ;;\nesac\n"
			path := filepath.Join(dir, "gh")
			if err := os.WriteFile(path, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			env := []string{
				"TERM=xterm-256color", "NO_COLOR=", "CLICOLOR=1", "CLICOLOR_FORCE=1", "FORCE_COLOR=3",
				"HOME=" + dir, "GH_CONFIG_DIR=" + dir, "GH_TOKEN=fixture-token", "GITHUB_TOKEN=fixture-fallback",
				"GH_HOST=github.com", "PATH=/usr/bin:/bin", "LINA_RESPONSES_API_KEY=fixture-provider-secret",
			}
			if tty != "" {
				env = append(env, "GH_FORCE_TTY="+tty)
			}
			original := slices.Clone(env)
			c, err := New(t.Context(), Config{Path: path, Env: env, Leaser: testLeaser{}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := c.Close(context.Background()); err != nil {
					t.Error(err)
				}
			})
			s, err := c.Read(t.Context(), Ref{"o/r", 140})
			if err != nil || s.Head != sha || s.State != "OPEN" || s.CI != "success" || !s.Done || len(s.Checks) != 1 || s.Checks[0].Name != "foundation" || s.Checks[0].Conclusion != "success" || len(s.Reviews) != 1 {
				t.Fatalf("plain child observation: %+v, error: %v", s, err)
			}
			data, err := os.ReadFile(filepath.Join(dir, "child-env"))
			if err != nil {
				t.Fatal(err)
			}
			want := []string{"unset", "1", "0", "unset", "unset", "xterm-256color", dir, dir, "fixture-token", "fixture-fallback", "github.com", "/usr/bin:/bin", ""}
			if string(data) != strings.Join(want, "\n")+"\n" {
				t.Fatalf("unexpected child environment: %q", data)
			}
			if !slices.Equal(env, original) {
				t.Fatal("changed caller's environment")
			}
			if data, err := os.ReadFile(config); err != nil || string(data) != login {
				t.Fatalf("changed stored login: %v", err)
			}
		})
	}
}
