// SPDX-License-Identifier: Apache-2.0

package modelpath

import (
	"errors"
	"path/filepath"
	"strings"
)

// LocalConfig is the on-device helper selected by the process environment.
// It is absent when no helper is configured.
type LocalConfig struct {
	Helper       string
	Model        string
	PrivateCloud bool
	Env          []string
}

const (
	onDeviceModel     = "apple-foundation-on-device"
	privateCloudModel = "apple-foundation-private-cloud"
)

// LocalFromEnv returns nil when the helper is not configured. A partial
// setting is refused. The helper is probed later, when Core starts.
func LocalFromEnv(getenv func(string) string) (*LocalConfig, error) {
	helper := getenv("LINA_APPLEFM_HELPER")
	model := getenv("LINA_APPLEFM_MODEL")
	cloud := getenv("LINA_APPLEFM_PRIVATE_CLOUD")
	if helper == "" && model == "" && cloud == "" {
		return nil, nil
	}
	if !filepath.IsAbs(helper) || strings.TrimSpace(model) != model || strings.ContainsAny(model, "\r\n\x00") {
		return nil, errors.New("invalid on-device model route")
	}
	switch cloud {
	case "", "1":
	default:
		return nil, errors.New("invalid on-device model route")
	}
	cfg := &LocalConfig{Helper: helper, PrivateCloud: cloud == "1"}
	switch {
	case model != "":
		cfg.Model = model
	case cfg.PrivateCloud:
		cfg.Model = privateCloudModel
	default:
		cfg.Model = onDeviceModel
	}
	for _, key := range []string{"HOME", "TMPDIR", "LANG", "LC_ALL"} {
		if value := getenv(key); value != "" && !strings.ContainsAny(value, "\r\n\x00") {
			cfg.Env = append(cfg.Env, key+"="+value)
		}
	}
	return cfg, nil
}
