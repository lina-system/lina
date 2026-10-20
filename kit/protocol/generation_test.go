// SPDX-License-Identifier: Apache-2.0

package protocol

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestGeneratedTypesCurrent(t *testing.T) {
	cmd := exec.Command("go", "run", "./internal/generate", "-check")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("regenerate client types: %v\n%s", err, output)
	}
}

func TestRegenerationRejectsDriftWithoutOverwriting(t *testing.T) {
	output := filepath.Join(t.TempDir(), "client.gen.go")
	cmd := exec.Command("go", "run", "./internal/generate", "-output", output)
	if log, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generate: %v\n%s", err, log)
	}
	original, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	drift := append(original, []byte("\n// stale generated output\n")...)
	if err := os.WriteFile(output, drift, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command("go", "run", "./internal/generate", "-output", output, "-check")
	if log, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("accepted stale generated types: %s", log)
	}
	after, err := os.ReadFile(output)
	if err != nil || string(after) != string(drift) {
		t.Fatal("checking generated types must not overwrite the candidate")
	}
}

func TestComponentsRejectGeneratedDriftBeforeBuild(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"go.mod", "kit/go.mod", "go.work"} {
		data, err := os.ReadFile(filepath.Join("../..", name))
		if err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "kit/protocol"), 0o700); err != nil {
		t.Fatal(err)
	}
	// Inject a failed regeneration command into the real components gate. Other
	// commands succeed, so a passing gate would mean it skipped the drift check.
	script := `
import assert from 'node:assert/strict';
import { resolve, join } from 'node:path';
import { pathToFileURL } from 'node:url';
const { checkComponents } = await import(pathToFileURL(resolve('../../scripts/verify/check-components.mjs')));
const calls = [];
const cwd = process.argv[1];
const result = checkComponents({ cwd, components: [{ name: 'kit', module: 'kit', packages: ['./...'] }],
  execute(command, args, options) {
    calls.push(args[0]);
    if (args[0] === 'run') {
      assert.ok(args.includes('-check'), 'regeneration must compare without rewriting');
      assert.equal(options.cwd, join(cwd, 'kit/protocol'));
      assert.equal(options.env.GH_TOKEN, undefined);
      return { status: 1, stdout: '', stderr: 'generated types differ\n' };
    }
    return { status: 0, stdout: args[0] === 'env' ? options.env.GOTOOLCHAIN + '\n' : '', stderr: '' };
  }
});
assert.equal(result.status, 1, 'components accepted stale generated types');
assert.match(result.output, /generated types differ/);
assert.ok(!calls.includes('build'), 'build ran after regeneration failed');
`
	cmd := exec.Command("node", "--input-type=module", "-e", script, root)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("components regeneration gate: %v\n%s", err, output)
	}
}
