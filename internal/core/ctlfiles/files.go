// SPDX-License-Identifier: Apache-2.0

// Package ctlfiles owns the durable, private files in system/core.
package ctlfiles

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
)

var ErrLocked = errors.New("another Core holds the state root lock")

// Store holds directory descriptors and, after Acquire, the stable lock inode.
// A Store has one owner; callers serialize its operations.
type Store struct {
	dir      *os.Root
	lock     *os.File
	lockName string
}

// Locked reports whether this owner holds the stable Core lock. Callers must
// keep it held for the complete lifetime of storage services they construct.
func (s *Store) Locked() bool { return s != nil && s.lock != nil && s.lockName == "core.lock" }

// Open validates an existing local state root and creates its private control
// directories. Installation owns creation of the root and main.json.
func Open(path string) (*Store, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("state root must be a directory, not a symlink")
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if err := checkDirectory(root); err != nil {
		return nil, err
	}
	for _, name := range []string{"system", "system/core"} {
		if err := root.Mkdir(name, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		info, err := root.Lstat(name)
		if err != nil {
			return nil, err
		}
		if err := private(info, true); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		child, err := root.OpenRoot(name)
		if err != nil {
			return nil, err
		}
		err = checkDirectory(child)
		child.Close()
		if err != nil {
			return nil, err
		}
	}
	// Persist the directory entries from the leaves upward before writing files.
	for _, name := range []string{"system/core", "system", "."} {
		if err := syncDirectory(root, name); err != nil {
			return nil, err
		}
	}
	dir, err := root.OpenRoot("system/core")
	if err != nil {
		return nil, err
	}
	return &Store{dir: dir}, nil
}

func checkDirectory(root *os.Root) error {
	file, err := root.Open(".")
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if err := checkLocal(file); err != nil {
		return err
	}
	return private(info, true)
}

// ValidatePrivateDirectory applies the state-directory owner and mode checks
// to directory metadata. Callers use Lstat so symbolic links are refused.
func ValidatePrivateDirectory(info os.FileInfo) error {
	return private(info, true)
}

func private(info os.FileInfo, directory bool) error {
	mode := os.FileMode(0600)
	valid := info.Mode().IsRegular()
	if directory {
		mode = 0700
		valid = info.IsDir()
	}
	if !valid || info.Mode().Perm() != mode || info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 || !owned(info, directory) {
		return fmt.Errorf("control path %s must be owned by the current user, private, and not linked (mode %s)", info.Name(), info.Mode())
	}
	return nil
}

// Acquire never replaces or unlinks core.lock: every contender must flock the
// same inode. Unlike data records, the lock is created once, then fsynced.
func (s *Store) Acquire() error {
	return s.acquire("core.lock")
}

// AcquireUpdate serializes update coordinators, independently of the running
// Core. It confers no canon authority. The owner writes only update.json.
func (s *Store) AcquireUpdate() error { return s.acquire("update.lock") }

func (s *Store) acquire(name string) error {
	if s.lock != nil {
		if s.lockName != name {
			return errors.New("control lock kind mismatch")
		}
		return nil
	}
	file, err := s.dir.OpenFile(name, os.O_CREATE|os.O_RDWR|noFollow, 0600)
	if err != nil {
		return err
	}
	success := false
	defer func() {
		if !success {
			file.Close()
		}
	}()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if err := private(info, false); err != nil {
		return err
	}
	if err := lockFile(file); err != nil {
		return err
	}
	current, err := s.dir.Lstat(name)
	if err != nil {
		return err
	}
	if !os.SameFile(info, current) {
		return errors.New("lock path changed during acquisition")
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := syncDirectory(s.dir, "."); err != nil {
		return err
	}
	s.lock = file
	s.lockName = name
	success = true
	return nil
}

func record(name string) bool {
	switch name {
	case "epoch", "run.sentinel", "halt.json", "lifecycle.json", "main.json", "config.json", "update.json", "a2a.json":
		return true
	}
	return false
}

func (s *Store) Read(name string) ([]byte, error) {
	return readRecord(name, s.dir.OpenFile, s.dir.Lstat)
}

func readRecord(name string, open func(string, int, os.FileMode) (*os.File, error), lstat func(string) (os.FileInfo, error)) ([]byte, error) {
	if !record(name) {
		return nil, errors.New("unknown control record")
	}
	for range readAttempts {
		data, retry, err := readOnce(name, open, lstat)
		if err != nil {
			return nil, err
		}
		if !retry {
			return data, nil
		}
	}
	return nil, errors.New("control record changed during read")
}

// readAttempts bounds the retries a record read spends on atomic replacement.
const readAttempts = 8

// readOnce validates and reads one attempt. retry reports that Write replaced
// the record mid-read, so the caller must reopen rather than accept the stale
// or unlinked inode.
func readOnce(name string, open func(string, int, os.FileMode) (*os.File, error), lstat func(string) (os.FileInfo, error)) ([]byte, bool, error) {
	// O_NONBLOCK prevents an unexpected FIFO from blocking before fstat rejects it.
	file, err := open(name, os.O_RDONLY|noFollow|nonBlock, 0)
	if err != nil {
		return nil, false, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, false, err
	}
	if err := private(info, false); err != nil {
		// Write renames over the record, leaving the opened inode unlinked.
		// Any other refusal is an unsafe inode, not a race.
		if unlinked(info) {
			return nil, true, nil
		}
		return nil, false, err
	}
	matched, err := namesInode(name, info, lstat)
	if err != nil {
		return nil, false, err
	}
	if !matched {
		return nil, true, nil
	}
	data, err := io.ReadAll(io.LimitReader(file, 65537))
	if err != nil {
		return nil, false, err
	}
	if len(data) > 65536 {
		return nil, false, errors.New("control record exceeds 64 KiB")
	}
	// Replacement fsyncs and renames a whole file, so a record whose bytes
	// streamed from a superseded inode is discarded.
	matched, err = namesInode(name, info, lstat)
	if err != nil {
		return nil, false, err
	}
	if !matched {
		return nil, true, nil
	}
	return data, false, nil
}

// namesInode checks the directory entry itself, which os.Root still resolves
// when a link's target lies inside the root, and matches it to the opened
// inode. A vanished entry was replaced and reports no match.
func namesInode(name string, opened os.FileInfo, lstat func(string) (os.FileInfo, error)) (bool, error) {
	current, err := lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := private(current, false); err != nil {
		return false, err
	}
	return os.SameFile(opened, current), nil
}

// Write publishes a whole record only after its temporary file is synced, then
// syncs the containing directory. On failure, callers must stop startup.
func (s *Store) Write(name string, data []byte) error {
	if s.lock == nil || (s.lockName != "core.lock" && name != "update.json") {
		return errors.New("control write requires the lock")
	}
	if !record(name) {
		return errors.New("unknown control record")
	}
	if len(data) > 65536 {
		return errors.New("control record exceeds 64 KiB")
	}
	if info, err := s.dir.Lstat(name); err == nil {
		if err := private(info, false); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	temporary := ".tmp-" + rand.Text()
	file, err := s.dir.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL|noFollow, 0600)
	if err != nil {
		return err
	}
	defer func() { file.Close(); s.dir.Remove(temporary) }()
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := s.dir.Rename(temporary, name); err != nil {
		return err
	}
	return syncDirectory(s.dir, ".")
}

// RemoveSentinel marks an orderly shutdown. The epoch and halt marker remain.
func (s *Store) RemoveSentinel() error {
	if !s.Locked() {
		return errors.New("control write requires the lock")
	}
	if err := s.dir.Remove("run.sentinel"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return syncDirectory(s.dir, ".")
}

// ClearHalt is used only after whole-generation restore verifies and reconciles
// the replacement. Ordinary startup and shutdown must retain the failure latch.
func (s *Store) ClearHalt() error {
	if !s.Locked() {
		return errors.New("control write requires the lock")
	}
	if _, err := s.Read("halt.json"); errors.Is(err, os.ErrNotExist) {
		return syncDirectory(s.dir, ".")
	} else if err != nil {
		return err
	}
	if err := s.dir.Remove("halt.json"); err != nil {
		return err
	}
	return syncDirectory(s.dir, ".")
}

func syncDirectory(root *os.Root, name string) error {
	directory, err := root.Open(name)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

// Epoch refuses malformed records instead of interpreting corruption as zero.
func (s *Store) Epoch() (uint64, error) {
	data, err := s.Read("epoch")
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	text := string(data)
	value, err := strconv.ParseUint(text, 10, 64)
	if err != nil || strconv.FormatUint(value, 10) != text {
		return 0, errors.New("invalid epoch record")
	}
	return value, nil
}

// MintEpoch persists max(file, read-only canon peek, peer seen maximum)+1.
func (s *Store) MintEpoch(canon, peer uint64) (uint64, error) {
	previous, err := s.Epoch()
	if err != nil {
		return 0, err
	}
	previous = max(previous, canon, peer)
	if previous == math.MaxUint64 {
		return 0, errors.New("epoch exhausted")
	}
	next := previous + 1
	if err := s.Write("epoch", []byte(strconv.FormatUint(next, 10))); err != nil {
		return 0, err
	}
	return next, nil
}

func (s *Store) Close() error {
	var err error
	if s.lock != nil {
		err = s.lock.Close()
		s.lock = nil
	}
	if s.dir != nil {
		err = errors.Join(err, s.dir.Close())
		s.dir = nil
	}
	return err
}
