// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/thisisjun786/lina/internal/core/boot"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/turn"
)

func runDoctor(args []string) int {
	flags := flag.NewFlagSet("lina doctor", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	root := flags.String("state-root", "", "installed state root (default: XDG_STATE_HOME/lina)")
	flags.Usage = func() { fmt.Fprintln(os.Stderr, "usage: lina doctor [--state-root <path>]") }
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		flags.Usage()
		return 64
	}
	if flags.NArg() != 0 {
		flags.Usage()
		return 64
	}
	if *root == "" {
		base := os.Getenv("XDG_STATE_HOME")
		if base == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				fmt.Fprintln(os.Stderr, "lina doctor: state root: cannot determine home directory")
				return 78
			}
			base = filepath.Join(home, ".local/state")
		}
		*root = filepath.Join(base, "lina")
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := diagnose(ctx, *root); err != nil {
		fmt.Fprintf(os.Stderr, "lina doctor: %v\n", err)
		return 78
	}
	fmt.Fprintln(os.Stdout, "OK: installed identity, control records and canon")
	return 0
}

// Doctor never opens the installed writer, acquires/creates core.lock, or
// contacts Core. SQLite may rebuild a WAL index even on a read-only connection,
// so inspect a private copy, including committed WAL, and discard it afterward.
// Compare the source again before reporting a result; a moving install is not
// evidence of either health or corruption. This also works during RECOVERING.
func diagnose(ctx context.Context, path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("state root: %w", err)
	}
	if err := ctlfiles.ValidatePrivateDirectory(info); err != nil {
		return fmt.Errorf("state root: %w", err)
	}
	source, err := os.OpenRoot(path)
	if err != nil {
		return fmt.Errorf("state root: %w", err)
	}
	defer source.Close()
	var filesystem syscall.Statfs_t
	if err := syscall.Statfs(path, &filesystem); err != nil {
		return fmt.Errorf("state root: %w", err)
	}
	// Same local filesystem allowlist as Core's control-file owner.
	switch uint64(filesystem.Type) {
	case 0xef53, 0x58465342, 0x9123683e, 0x01021994, 0x794c7630, 0xf2f52010, 0x2fc12fc1, 0x3434, 0x52654973, 0x3153464a:
	default:
		return errors.New("state root: requires a supported local filesystem")
	}
	for _, name := range []string{"system", "system/core", "canon"} {
		info, err := source.Lstat(name)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if err := ctlfiles.ValidatePrivateDirectory(info); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	if _, err := source.Lstat("canon/canon.sqlite"); errors.Is(err, os.ErrNotExist) {
		directory, err := source.Open("canon")
		if err != nil {
			return fmt.Errorf("canon: %w", err)
		}
		entries, readErr := directory.ReadDir(-1)
		closeErr := directory.Close()
		if err := errors.Join(readErr, closeErr); err != nil {
			return fmt.Errorf("canon: %w", err)
		}
		if len(entries) != 0 {
			return errors.New("canon: database is missing but canon is not empty")
		}
	}
	temporary, err := os.MkdirTemp("", "lina-doctor-")
	if err != nil {
		return fmt.Errorf("diagnosis workspace: %w", err)
	}
	defer os.RemoveAll(temporary)
	for _, name := range []string{"system/core", "canon"} {
		if err := os.MkdirAll(filepath.Join(temporary, name), 0700); err != nil {
			return fmt.Errorf("diagnosis workspace: %w", err)
		}
	}
	var files []doctorCopy
	for _, name := range []string{"system/core/main.json", "system/core/core.lock", "system/core/epoch", "system/core/run.sentinel", "system/core/halt.json", "system/core/lifecycle.json", "canon/canon.sqlite", "canon/canon.sqlite-wal", "canon/canon.sqlite-journal"} {
		if err := ctx.Err(); err != nil {
			return err
		}
		file, err := copyDoctorFile(source, temporary, name)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		files = append(files, file)
	}
	result := inspectDoctorCopy(ctx, temporary)
	for _, file := range files {
		if err := file.unchanged(source); err != nil {
			return fmt.Errorf("%s: state changed during diagnosis; retry: %w", file.name, err)
		}
	}
	return result
}

type doctorCopy struct {
	name string
	info os.FileInfo
	hash [32]byte
}

func copyDoctorFile(source *os.Root, destination, name string) (doctorCopy, error) {
	copy := doctorCopy{name: name}
	info, err := source.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return copy, nil
	}
	if err != nil {
		return copy, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || !ok || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 {
		return copy, errors.New("must be a regular file owned by the current user, without links")
	}
	if strings.HasPrefix(name, "system/") {
		if info.Mode() != 0600 {
			return copy, errors.New("control file must have private mode 0600")
		}
		if info.Size() > 65536 {
			return copy, errors.New("control record exceeds 64 KiB")
		}
	}
	input, err := source.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return copy, err
	}
	defer input.Close()
	opened, err := input.Stat()
	if err != nil {
		return copy, err
	}
	if !os.SameFile(info, opened) {
		return copy, errors.New("file changed during diagnosis; retry")
	}
	output, err := os.OpenFile(filepath.Join(destination, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return copy, err
	}
	hash := sha256.New()
	_, readErr := io.CopyN(io.MultiWriter(output, hash), input, info.Size())
	if err := errors.Join(readErr, output.Close()); err != nil {
		return copy, err
	}
	copy.info = info
	copy.hash = [32]byte(hash.Sum(nil))
	return copy, nil
}

func (copy doctorCopy) unchanged(source *os.Root) error {
	info, err := source.Lstat(copy.name)
	if copy.info == nil && errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if copy.info == nil || !os.SameFile(copy.info, info) || copy.info.Mode() != info.Mode() || copy.info.Size() != info.Size() || !copy.info.ModTime().Equal(info.ModTime()) {
		return errors.New("file replaced or modified")
	}
	file, err := source.OpenFile(copy.name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.CopyN(hash, file, info.Size()); err != nil {
		return err
	}
	if copy.hash != [32]byte(hash.Sum(nil)) {
		return errors.New("file content changed")
	}
	return nil
}

func inspectDoctorCopy(ctx context.Context, root string) error {
	controls, err := ctlfiles.Open(root)
	if err != nil {
		return fmt.Errorf("control records: %w", err)
	}
	defer controls.Close()
	data, err := controls.Read("main.json")
	if err != nil {
		return fmt.Errorf("main.json: %w", err)
	}
	var identity struct {
		ID string `json:"main_id"`
	}
	if err := json.Unmarshal(data, &identity); err != nil {
		return fmt.Errorf("main.json: invalid installed identity: %w", err)
	}
	if identity.ID == "" || strings.TrimSpace(identity.ID) != identity.ID || len(identity.ID) > 256 {
		return errors.New("main.json: invalid installed identity")
	}
	epoch, err := controls.Epoch()
	if err != nil {
		return fmt.Errorf("epoch: %w", err)
	}
	if epoch == math.MaxUint64 {
		return errors.New("epoch: exhausted; Core cannot mint another epoch")
	}
	if _, err := controls.ReadRestart(); err != nil {
		return fmt.Errorf("lifecycle.json: %w", err)
	}
	if data, err := controls.Read("run.sentinel"); err == nil {
		var previous boot.Sentinel
		var object map[string]json.RawMessage
		if json.Unmarshal(data, &previous) != nil || json.Unmarshal(data, &object) != nil || object == nil {
			return errors.New("run.sentinel: malformed record")
		}
		epoch, err := strconv.ParseUint(previous.Epoch, 10, 64)
		if err != nil || epoch == 0 || strconv.FormatUint(epoch, 10) != previous.Epoch || len(object["socket"]) == 0 || string(object["socket"]) == "null" || string(object["pending_socket"]) == "null" || (previous.Socket != "" && !filepath.IsAbs(previous.Socket)) || (previous.PendingSocket != "" && !filepath.IsAbs(previous.PendingSocket)) {
			return errors.New("run.sentinel: invalid epoch or socket record")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("run.sentinel: %w", err)
	}
	// Keep inspecting canon even with a halt marker, so corruption is diagnosed
	// rather than obscured by the record of a previous failure.
	canonErr := inspectDoctorCanon(ctx, root)
	if data, err := controls.Read("halt.json"); err == nil {
		var marker struct {
			Cause string `json:"cause"`
		}
		if json.Unmarshal(data, &marker) != nil {
			return errors.Join(canonErr, errors.New("halt.json: malformed halt marker; writes remain halted"))
		}
		cause := "halt marker is present; writes remain halted until a verified restore"
		switch marker.Cause {
		case "sqlite_corrupt":
			cause = "database corruption halted writes; a verified restore is required"
		case "sqlite_notadb":
			cause = "canon is not a SQLite database; a verified restore is required"
		case "sqlite_io":
			cause = "storage I/O failure halted writes; a verified restore is required"
		}
		return errors.Join(canonErr, fmt.Errorf("halt.json: %s", cause))
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.Join(canonErr, fmt.Errorf("halt.json: %w", err))
	}
	return canonErr
}

func inspectDoctorCanon(ctx context.Context, root string) error {
	path := filepath.Join(root, "canon/canon.sqlite")
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		entries, err := os.ReadDir(filepath.Dir(path))
		if err != nil {
			return fmt.Errorf("canon: %w", err)
		}
		if len(entries) == 0 {
			return nil
		} // Fresh install, before first canon use.
		return errors.New("canon: database is missing but journal files remain")
	} else if err != nil {
		return fmt.Errorf("canon: %w", err)
	}
	version, epoch, err := canon.Inspect(ctx, path)
	if err != nil {
		return fmt.Errorf("canon: %w", err)
	}
	// Match the application schema composed by boot.startConversation.
	maxVersion := len(canon.Migrations()) + len([]string{turn.Schema, turn.ControlSchema, turn.OutcomeSchema})
	if version > maxVersion {
		return errors.New("canon: database schema is newer than this binary supports")
	}
	if epoch == math.MaxUint64 {
		return errors.New("canon: epoch exhausted; Core cannot mint another epoch")
	}
	db, err := canon.OpenSQLiteReader(ctx, path)
	if err != nil {
		return fmt.Errorf("canon: %w", err)
	}
	defer db.Close()
	var metadata, tables int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_schema WHERE name='canon_meta'").Scan(&metadata); err != nil {
		return fmt.Errorf("canon: %w", err)
	}
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%'").Scan(&tables); err != nil {
		return fmt.Errorf("canon: %w", err)
	}
	if metadata == 0 && (tables != 0 || version != 0) {
		return errors.New("canon: database has no canon metadata")
	}
	rows, err := db.QueryContext(ctx, "PRAGMA integrity_check")
	if err != nil {
		return fmt.Errorf("canon: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var result string
		if err := rows.Scan(&result); err != nil {
			return fmt.Errorf("canon: %w", err)
		}
		if result != "ok" {
			return errors.New("canon: database integrity check failed")
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("canon: %w", err)
	}
	return nil
}
