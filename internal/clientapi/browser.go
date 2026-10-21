// SPDX-License-Identifier: Apache-2.0

package clientapi

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/proc"
)

func SignInURLAllowed(raw string) bool {
	if len(raw) > 16384 || strings.ContainsAny(raw, "\x00\r\n\x1b") {
		return false
	}
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil
}

func OpenSignInBrowser(ctx context.Context, raw string) error {
	if !SignInURLAllowed(raw) {
		return ErrMalformed
	}
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	var path string
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if !filepath.IsAbs(dir) {
			continue
		}
		candidate := filepath.Join(dir, name)
		info, err := os.Stat(candidate)
		if err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0 {
			path = candidate
			break
		}
	}
	if path == "" {
		return os.ErrNotExist
	}
	owner := proc.NewOwner()
	defer owner.Close(context.Background())
	activity := lease.New(func() {}) // Client-local browser lifetime; no Core work is admitted.
	defer activity.Release()
	return owner.Run(ctx, activity, proc.Command{Path: path, Args: []string{raw}, Env: proc.InheritedEnvironment()})
}
