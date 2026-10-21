// SPDX-License-Identifier: Apache-2.0
package runs

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// All run-owned paths are confined to Core's private work root. Tool adapters
// receive a run's scratch path through Record, never a model-supplied path.
func openWork(path string) (*os.Root, error) {
	if path == "" {
		return nil, ErrInvalid
	}
	if err := os.Mkdir(path, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode().Perm() != 0700 {
		return nil, ErrInvalid
	}
	return os.OpenRoot(path)
}
func (s *Service) prepareFiles(id string) error {
	for _, path := range []string{"runs", filepath.Join("runs", id), filepath.Join("runs", id, "scratch")} {
		if err := s.root.Mkdir(path, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		info, err := s.root.Lstat(path)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode().Perm() != 0700 {
			return ErrInvalid
		}
	}
	return nil
}
func (s *Service) transcript(id, kind string, value any) error {
	body, err := json.Marshal(struct {
		Kind  string
		Value any
	}{kind, value})
	if err != nil {
		return err
	}
	path := filepath.Join("runs", id, "transcript.jsonl")
	if info, err := s.root.Lstat(path); err == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
			return ErrInvalid
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	f, err := s.root.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(append(body, '\n'))
	if err == nil {
		err = f.Sync()
	}
	return errors.Join(err, f.Close())
}
