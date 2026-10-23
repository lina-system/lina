// SPDX-License-Identifier: Apache-2.0
package install

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"time"

	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/proc"
)

const claudeACPEntry = "node_modules/@agentclientprotocol/claude-agent-acp/dist/index.js"
const npmConfig = "registry=https://registry.npmjs.org/\nsave-exact=true\nignore-scripts=true\n"

var claudeACPInstall = make(chan struct{}, 1)

type npmPackage struct {
	Version   string `json:"version"`
	Resolved  string `json:"resolved"`
	Integrity string `json:"integrity"`
	Optional  bool   `json:"optional"`
	Link      bool   `json:"link"`
}
type npmLock struct {
	Version  int                   `json:"lockfileVersion"`
	Packages map[string]npmPackage `json:"packages"`
}

// ClaudeACP installs only the release's lockfile, on first selected use. npm
// verifies tarballs (including cache hits) against their SHA-512 integrity.
// A private staging tree prevents a failed/cancelled install becoming runnable.
func ClaudeACP(ctx context.Context, l lease.Lease, source string) (string, error) {
	select {
	case claudeACPInstall <- struct{}{}:
		defer func() { <-claudeACPInstall }()
	case <-ctx.Done():
		return "", ctx.Err()
	}
	if !filepath.IsAbs(source) {
		return "", errors.New("Claude ACP requires an absolute release directory")
	}
	manifest, err := os.ReadFile(filepath.Join(source, "package.json"))
	if err != nil {
		return "", fmt.Errorf("Claude ACP release manifest unavailable: %w", err)
	}
	raw, err := os.ReadFile(filepath.Join(source, "package-lock.json"))
	if err != nil {
		return "", fmt.Errorf("Claude ACP release lockfile unavailable: %w", err)
	}
	lock, err := readNPMLock(raw)
	if err != nil {
		return "", err
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(append(append([]byte(nil), manifest...), raw...)))
	parent := filepath.Join(source, "runtime")
	target := filepath.Join(parent, digest)
	if _, err := os.Lstat(target); err == nil {
		if err := verifyNPMTree(target, lock); err != nil {
			return "", err
		}
		return filepath.Join(target, claudeACPEntry), nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	npm, err := proc.LookPath("npm")
	if err != nil {
		return "", errors.New("Claude ACP requires npm on PATH")
	}
	if err := os.MkdirAll(parent, 0700); err != nil {
		return "", err
	}
	stage, err := os.MkdirTemp(parent, ".install-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(stage)
	for name, data := range map[string][]byte{"package.json": manifest, "package-lock.json": raw, ".npmrc": []byte(npmConfig)} {
		if err := os.WriteFile(filepath.Join(stage, name), data, 0600); err != nil {
			return "", err
		}
	}
	// HOME and npm cache/config are disposable; user registry credentials,
	// lifecycle overrides and Node injection variables never enter npm.
	home, err := os.MkdirTemp(parent, ".npm-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(home)
	env := []string{"HOME=" + home, "USERPROFILE=" + home, "XDG_CONFIG_HOME=" + home}
	for _, name := range []string{"PATH", "SystemRoot", "SYSTEMROOT", "WINDIR", "TMPDIR", "TMP", "TEMP"} {
		if value, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+value)
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	owner := proc.NewOwner()
	defer owner.Close(context.Background())
	err = owner.Run(ctx, l, proc.Command{Path: npm, Dir: stage, Env: env, Args: []string{"ci", "--ignore-scripts", "--omit=dev", "--omit=optional", "--registry=https://registry.npmjs.org/", "--userconfig=" + filepath.Join(home, "user.npmrc"), "--globalconfig=" + filepath.Join(home, "global.npmrc"), "--cache=" + filepath.Join(home, "cache"), "--no-audit", "--no-fund"}, Stdout: io.Discard, Stderr: io.Discard})
	if err != nil {
		return "", fmt.Errorf("Claude ACP locked npm install failed: %w", err)
	}
	if err := checkNPMPackages(stage, lock); err != nil {
		return "", err
	}
	files, err := npmTree(stage)
	if err != nil {
		return "", err
	}
	seal, _ := json.Marshal(files)
	if err := os.WriteFile(filepath.Join(stage, ".lina-integrity.json"), seal, 0600); err != nil {
		return "", err
	}
	if err := os.Rename(stage, target); err != nil {
		// Another Core using the same immutable release may have completed first.
		if err := verifyNPMTree(target, lock); err != nil {
			return "", err
		}
	}
	return filepath.Join(target, claudeACPEntry), nil
}

func readNPMLock(raw []byte) (npmLock, error) {
	var lock npmLock
	if err := json.Unmarshal(raw, &lock); err != nil {
		return lock, err
	}
	if lock.Version != 3 || lock.Packages[""].Version == "" || lock.Packages["node_modules/@agentclientprotocol/claude-agent-acp"].Version == "" {
		return lock, errors.New("invalid Claude ACP lockfile")
	}
	integrity := regexp.MustCompile(`^sha512-[A-Za-z0-9+/]{86}==$`)
	for name, pkg := range lock.Packages {
		if name == "" {
			continue
		}
		u, err := url.Parse(pkg.Resolved)
		if !strings.HasPrefix(name, "node_modules/") || !filepath.IsLocal(name) || filepath.ToSlash(filepath.Clean(name)) != name || strings.Contains(name, "\\") || pkg.Link || err != nil || u.Scheme != "https" || u.Host != "registry.npmjs.org" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || !integrity.MatchString(pkg.Integrity) {
			return lock, errors.New("untrusted Claude ACP lockfile entry")
		}
	}
	return lock, nil
}
func checkNPMPackages(root string, lock npmLock) error {
	for name, pkg := range lock.Packages {
		if name == "" {
			continue
		}
		_, err := os.Lstat(filepath.Join(root, filepath.FromSlash(name)))
		if pkg.Optional {
			if !errors.Is(err, os.ErrNotExist) {
				return errors.New("optional package present in Claude ACP installation")
			}
		} else if err != nil {
			return fmt.Errorf("Claude ACP dependency missing: %s", name)
		}
	}
	info, err := os.Lstat(filepath.Join(root, claudeACPEntry))
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("Claude ACP entry point missing")
	}
	return nil
}

// Seal every installed byte and symlink, so reuse cannot quietly accept a
// partial, edited or different dependency tree. Symlinks must stay in the tree.
func npmTree(root string) (map[string]string, error) {
	files := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == ".lina-integrity.json" {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			resolved, err := filepath.EvalSymlinks(path)
			if err != nil {
				return err
			}
			inside, err := filepath.Rel(root, resolved)
			if err != nil || !filepath.IsLocal(inside) {
				return errors.New("Claude ACP symlink escapes installation")
			}
			files[rel] = "link:" + link
			return nil
		}
		if !entry.Type().IsRegular() {
			return errors.New("Claude ACP installation has special file")
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		hash := sha256.New()
		_, err = io.Copy(hash, f)
		closeErr := f.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		files[rel] = fmt.Sprintf("%x", hash.Sum(nil))
		return nil
	})
	return files, err
}
func verifyNPMTree(root string, lock npmLock) error {
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() {
		return errors.New("invalid Claude ACP installation directory")
	}
	if err := checkNPMPackages(root, lock); err != nil {
		return err
	}
	raw, err := os.ReadFile(filepath.Join(root, ".lina-integrity.json"))
	if err != nil {
		return err
	}
	var want map[string]string
	if err := json.Unmarshal(raw, &want); err != nil {
		return err
	}
	got, err := npmTree(root)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(want, got) {
		return errors.New("Claude ACP installed integrity mismatch")
	}
	return nil
}
