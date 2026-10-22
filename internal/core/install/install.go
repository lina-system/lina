// SPDX-License-Identifier: Apache-2.0

// Package install creates a new private Core installation without signing in.
package install

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/persona"
)

// Host is the process boundary. Tests use a scripted service manager and tools.
type Host interface {
	LookPath(string) (string, error)
	Run(context.Context, string, ...string) ([]byte, error)
}

type Config struct {
	StateRoot     string
	UnitDir       string
	BundleDir     string
	Socket        string
	ListenAddress string
	ToolPaths     map[string]string
	PersonaSource string
}

type Tool struct {
	Name             string   `json:"name"`
	Path             string   `json:"path,omitempty"`
	Version          string   `json:"version,omitempty"`
	Status           string   `json:"status"`
	VerifiedVersions []string `json:"verified_versions"`
}

type Result struct {
	MainID      string   `json:"main_id"`
	Socket      string   `json:"socket"`
	Tools       []Tool   `json:"tools"`
	Unavailable []string `json:"unavailable"`
}

func Run(ctx context.Context, cfg Config, host Host) (result Result, resultErr error) {
	if runtime.GOOS != "linux" {
		return result, errors.New("Core installation requires Linux")
	}
	if host == nil {
		return result, errors.New("installation requires a process host")
	}
	coreConfig := ctlfiles.Config{PersonaSource: cfg.PersonaSource}
	if err := coreConfig.Validate(); err != nil {
		return result, err
	}
	for _, path := range []string{cfg.StateRoot, cfg.UnitDir, cfg.BundleDir} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsAny(path, "\x00\r\n") {
			return result, errors.New("installation paths must be clean absolute paths")
		}
	}
	if cfg.Socket == "" {
		cfg.Socket = filepath.Join(cfg.StateRoot, "system/run/client.sock")
	}
	if !filepath.IsAbs(cfg.Socket) || filepath.Clean(cfg.Socket) != cfg.Socket || len(cfg.Socket) > 107 {
		return result, errors.New("client socket needs an absolute path of at most 107 bytes")
	}
	for name, path := range cfg.ToolPaths {
		if !slices.Contains(toolNames, name) || !filepath.IsAbs(path) {
			return result, errors.New("tool overrides require a known tool and absolute path")
		}
	}
	if err := absent(cfg.StateRoot); err != nil {
		return result, err
	}
	if err := absent(cfg.Socket); err != nil {
		return result, err
	}
	for _, path := range []string{filepath.Dir(cfg.StateRoot), cfg.UnitDir, filepath.Dir(cfg.Socket)} {
		if err := safeParents(path); err != nil {
			return result, err
		}
	}
	if info, err := os.Lstat(filepath.Dir(cfg.Socket)); err == nil {
		if err := ctlfiles.ValidatePrivateDirectory(info); err != nil {
			return result, fmt.Errorf("client socket directory: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return result, err
	}
	b, err := readBundle(cfg.BundleDir)
	if err != nil {
		return result, fmt.Errorf("bundle: %w", err)
	}
	units := []string{"lina-core.service"}
	if b.files["moirai-worker"] != nil {
		units = append(units, "lina-moirai.service")
	}
	if err := validateSocketLocation(cfg, units); err != nil {
		return result, err
	}
	systemctl, err := host.LookPath("systemctl")
	if err != nil {
		return result, errors.New("systemd user service manager is unavailable")
	}
	for _, unit := range units {
		for _, path := range []string{filepath.Join(cfg.UnitDir, unit), filepath.Join(cfg.UnitDir, unit+".d"), filepath.Join(cfg.UnitDir, "default.target.wants", unit)} {
			if err := absent(path); err != nil {
				return result, err
			}
		}
		out, err := host.Run(ctx, systemctl, "--user", "show", unit, "--property=LoadState", "--value")
		if err != nil {
			return result, errors.New("cannot inspect the systemd user service manager")
		}
		if strings.TrimSpace(string(out)) != "not-found" {
			return result, fmt.Errorf("service already exists: %s", unit)
		}
	}
	// Hold the probe until files are published, so another listener cannot take
	// the port between preflight and publication. Core currently uses Unix IPC.
	if cfg.ListenAddress != "" {
		listener, err := net.Listen("tcp", cfg.ListenAddress)
		if err != nil {
			return result, fmt.Errorf("listen address unavailable: %w", err)
		}
		defer listener.Close()
	}
	unit, err := coreUnit(filepath.Join(cfg.StateRoot, "system/bin/lina"), cfg.StateRoot, cfg.Socket)
	if err != nil {
		return result, err
	}
	contents := map[string]string{"lina-core.service": unit}
	if b.files["moirai-worker"] != nil {
		workerSocket := filepath.Join(cfg.StateRoot, "run/jobs.sock")
		if len(workerSocket) > 107 {
			return result, errors.New("worker socket path exceeds 107 bytes")
		}
		contents["lina-moirai.service"], err = workerUnit(filepath.Join(cfg.StateRoot, "system/bin/moirai-worker"), cfg.StateRoot, workerSocket)
		if err != nil {
			return result, err
		}
	}
	result.Tools = discover(ctx, cfg, host, b.verified)
	if err := ctx.Err(); err != nil {
		return result, err
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return result, err
	}
	result.MainID = "main-" + hex.EncodeToString(id[:])
	result.Socket = cfg.Socket
	if _, _, err := persona.LoadLina(cfg.PersonaSource); err != nil {
		result.Unavailable = append(result.Unavailable, persona.SetupWarning)
	}
	if b.files["adapters/claude/package-lock.json"] == nil {
		result.Unavailable = append(result.Unavailable, "Claude adapter files are not included in this build")
	}
	if b.files["moirai-worker"] == nil {
		result.Unavailable = append(result.Unavailable, "Background memory and planning: moirai-worker is not included")
	}
	var changes changes
	serviceAttempted := false
	defer func() {
		if resultErr != nil && !serviceAttempted {
			resultErr = errors.Join(resultErr, changes.rollback())
		}
	}()
	// The exclusive root creation is also the installer lock. Never merge a new
	// identity or configuration into another install, even one racing this call.
	if err := changes.mkdir(filepath.Dir(cfg.StateRoot)); err != nil {
		return result, err
	}
	if err := os.Mkdir(cfg.StateRoot, 0700); err != nil {
		return result, err
	}
	if err := changes.record(cfg.StateRoot); err != nil {
		return result, err
	}
	for _, name := range []string{"canon", "versions/agents", "versions/records", "materials/user", "materials/lina", "derived", "workspace", "work/clones", "work/tasks", "work/runs", "plugins", "staging/import", "staging/export", "system/core", "system/run", "secrets", "backups"} {
		if err := changes.mkdir(filepath.Join(cfg.StateRoot, name)); err != nil {
			return result, err
		}
	}
	if err := changes.mkdir(filepath.Dir(cfg.Socket)); err != nil {
		return result, err
	}
	control, err := ctlfiles.Open(cfg.StateRoot)
	if err != nil {
		return result, err
	}
	control.Close()
	names := make([]string, 0, len(b.files))
	for name := range b.files {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		dest := filepath.Join(cfg.StateRoot, "system/bin", name)
		mode := os.FileMode(0600)
		if name == "lina" || name == "moirai-worker" {
			mode = 0700
		}
		if strings.HasPrefix(name, "adapters/") {
			dest = filepath.Join(cfg.StateRoot, "system", name)
		}
		if err := changes.write(dest, b.files[name], mode); err != nil {
			return result, err
		}
	}
	for name, value := range map[string]any{"system/core/main.json": map[string]string{"main_id": result.MainID}, "system/core/config.json": coreConfig, "system/tools.json": result.Tools, "system/install.json": result} {
		data, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			return result, err
		}
		if err := changes.write(filepath.Join(cfg.StateRoot, name), append(data, '\n'), 0600); err != nil {
			return result, err
		}
	}
	for _, name := range units {
		if err := changes.write(filepath.Join(cfg.UnitDir, name), []byte(contents[name]), 0600); err != nil {
			return result, err
		}
	}
	if _, err := host.Run(ctx, systemctl, "--user", "daemon-reload"); err != nil {
		return result, errors.New("systemd could not reload the new service")
	}
	serviceAttempted = true
	// Core owns canon and the job socket; the optional worker follows its service.
	if _, err := host.Run(ctx, systemctl, "--user", "enable", "--now", filepath.Join(cfg.UnitDir, "lina-core.service")); err != nil {
		return result, errors.New("systemd could not enable and start Core; the new installation is retained for diagnosis")
	}
	if b.files["moirai-worker"] != nil {
		if _, err := host.Run(ctx, systemctl, "--user", "enable", "--now", filepath.Join(cfg.UnitDir, "lina-moirai.service")); err != nil {
			return result, errors.New("systemd could not start the worker; installation retained for diagnosis")
		}
	}
	return result, nil
}

func withinPath(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	return err == nil && (rel == "." || filepath.IsLocal(rel))
}

func validateSocketLocation(cfg Config, units []string) error {
	// Core owns the other state areas and creates control files during boot.
	// Keep sockets in the run area or a separately selected private directory.
	run := filepath.Join(cfg.StateRoot, "system/run")
	if withinPath(cfg.Socket, cfg.StateRoot) || (withinPath(cfg.StateRoot, cfg.Socket) && (cfg.Socket == run || !withinPath(run, cfg.Socket))) {
		return errors.New("a client socket inside the state root must be below system/run")
	}
	reserved := []string{cfg.UnitDir, filepath.Join(cfg.StateRoot, "run/jobs.sock")}
	for _, name := range units {
		reserved = append(reserved, filepath.Join(cfg.UnitDir, name), filepath.Join(cfg.UnitDir, name+".d"), filepath.Join(cfg.UnitDir, "default.target.wants", name))
	}
	for _, path := range reserved {
		if withinPath(cfg.Socket, path) {
			return errors.New("client socket conflicts with an installation path")
		}
	}
	return nil
}
