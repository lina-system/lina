// SPDX-License-Identifier: Apache-2.0
package results

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"github.com/thisisjun786/lina/internal/core/assets"
	"github.com/thisisjun786/lina/internal/core/records"
	"github.com/thisisjun786/lina/kit/result"
)

var ErrRecordsChanged = records.ErrRecordsChanged

// ResolvePlace is a trusted lookup of a container id to its fixed PARA folder.
// It must check the person's ownership. Model input never supplies a path.
type ResolvePlace func(context.Context, result.Scope, result.Destination) (string, error)
type Records struct {
	repo             records.Repo
	store            *records.Store
	assets           *assets.Store
	resolve          ResolvePlace
	afterPublication func(string) error
}

var recordsMu sync.Mutex

func OpenRecords(stateRoot string, db Canon, a *assets.Store, resolve ResolvePlace) (*Records, error) {
	store, err := records.Open(stateRoot, db)
	if err != nil {
		return nil, err
	}
	r := &Records{repo: store, store: store, assets: a, resolve: resolve}
	store.PublicationHook = func(stage string) error {
		if r.afterPublication != nil {
			return r.afterPublication(stage)
		}
		return nil
	}
	return r, nil
}
func (r *Records) Close() error { return r.store.Close() }
func slug(title string) string {
	var b strings.Builder
	dash := false
	for _, c := range strings.ToLower(title) {
		if unicode.IsLetter(c) || unicode.IsDigit(c) {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = false
			b.WriteRune(c)
		} else {
			dash = true
		}
		if b.Len() > 80 {
			break
		}
	}
	if b.Len() == 0 {
		return "result"
	}
	return b.String()
}
func (r *Records) folder(ctx context.Context, s result.Scope, d result.Destination) (string, error) {
	switch d.Kind {
	case "memo":
		if d.Container == "" {
			return "inbox", nil
		}
	case "resource":
		if d.Container == "" {
			return "resources", nil
		}
	case "project", "area", "goal":
		if r.resolve != nil && d.Container != "" {
			dir, err := r.resolve(ctx, s, d)
			if err != nil {
				return "", err
			}
			if fs.ValidPath(dir) && dir != "." && !strings.HasPrefix(dir, ".git") {
				prefix := strings.Split(dir, "/")[0]
				if prefix == "projects" || prefix == "areas" || prefix == "resources" || prefix == "archive" {
					return dir, nil
				}
			}
		}
	}
	return "", ErrOutOfScope
}
func recordBytes(scope result.Scope, r result.Result, ref result.SavedRef) []byte {
	r.Status = "saved"
	r.SavedAs = &ref
	id, _ := json.Marshal(ref.ID)
	owner, _ := json.Marshal(scope.PersonID)
	agent, _ := json.Marshal(scope.AgentID)
	raw, _ := json.Marshal(r)
	kind := "document"
	if r.Type == result.SummaryType {
		kind = "memo"
	}
	return []byte(fmt.Sprintf("---\nid: %s\nkind: %s\nstate: active\nversion: 1\nperson_id: %s\nagent_id: %s\nresult: %s\n---\n\n# %s\n\n%s\n", id, kind, owner, agent, raw, r.Title, r.Text))
}

// Prepare performs read-only checks before the result store reserves the save.
// The closure keeps the authorized folder; it never resolves a different place.
func (r *Records) Prepare(ctx context.Context, s result.Scope, value result.Result, d result.Destination) (PreparedSave, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if value.ID == "" || value.Revision <= 0 || !validScope(s) {
		return nil, result.ErrMalformed
	}
	if _, err := result.Validate(value.Type, value.Body, result.Limits{}); err != nil {
		return nil, err
	}
	folder, err := r.folder(ctx, s, d)
	if err != nil {
		return nil, err
	}
	if value.Type == result.FileType {
		if r.assets == nil {
			return nil, ErrUnavailable
		}
		var f result.File
		if err := json.Unmarshal(value.Body, &f); err != nil {
			return nil, err
		}
		a, err := r.assets.Get(ctx, f.Asset.ID)
		if err != nil {
			return nil, err
		}
		if a.Actor.PersonID != s.PersonID {
			return nil, ErrOutOfScope
		}
		stream, err := r.assets.OpenRead(ctx, f.Asset.ID, int64(f.Asset.Revision))
		if err != nil {
			return nil, err
		}
		size, err := io.Copy(io.Discard, stream)
		err = errors.Join(err, stream.Close())
		if err != nil {
			return nil, err
		}
		if size != f.Size {
			return nil, result.ErrMalformed
		}
	}
	value.Body = append(json.RawMessage(nil), value.Body...)
	return func(ctx context.Context, ref result.SavedRef) error {
		if ref.Destination != d {
			return ErrOutOfScope
		}
		return r.saveResolved(ctx, s, value, ref, folder)
	}, nil
}
func (r *Records) Save(ctx context.Context, s result.Scope, value result.Result, ref result.SavedRef) error {
	publish, err := r.Prepare(ctx, s, value, ref.Destination)
	if err != nil {
		return err
	}
	return publish(ctx, ref)
}
func (r *Records) saveResolved(ctx context.Context, s result.Scope, value result.Result, ref result.SavedRef, folder string) error {
	recordsMu.Lock()
	defer recordsMu.Unlock()
	if ref.ID == "" || ref.Version != 1 {
		return result.ErrMalformed
	}
	data := recordBytes(s, value, ref)
	old, _, err := r.repo.Read(ctx, ref.ID)
	if err == nil {
		if bytes.Equal(old.Data, data) {
			return nil
		}
		return ErrRecordsChanged
	}
	if !errors.Is(err, records.ErrNotFound) {
		return err
	}
	// A file preview gets independent owned bytes, so deleting the conversation
	// can erase its original without removing the saved material.
	if value.Type == result.FileType {
		if r.assets == nil {
			return ErrUnavailable
		}
		var f result.File
		if err := json.Unmarshal(value.Body, &f); err != nil {
			return err
		}
		a, err := r.assets.Get(ctx, f.Asset.ID)
		if err != nil {
			return err
		}
		if a.Actor.PersonID != s.PersonID {
			return ErrOutOfScope
		}
		input, err := r.assets.OpenRead(ctx, f.Asset.ID, int64(f.Asset.Revision))
		if err != nil {
			return err
		}
		token, err := r.assets.StageImport(ctx, input)
		err = errors.Join(err, input.Close())
		if err != nil {
			return err
		}
		saved, err := r.assets.Handover(ctx, token, f.Name, assets.Lina, assets.Actor{PersonID: s.PersonID, AgentID: s.AgentID})
		if err != nil {
			return err
		}
		if saved.Size != f.Size {
			return result.ErrMalformed
		}
		f.Asset = result.AssetRef{ID: saved.ID, Revision: int(saved.Revision)}
		value.Body, _ = json.Marshal(f)
		value.Text = result.PlainText(&f, nil)
	}

	data = recordBytes(s, value, ref)
	stem := slug(value.Title)
	// The repository chooses a free slug atomically with the commit. Results
	// carry the existing Core-allocated saved id; no user path becomes authority.
	for n := 1; ; n++ {
		name := stem
		if n > 1 {
			name += "-" + strconv.Itoa(n)
		}
		_, err = r.repo.Commit(ctx, records.Change{ID: ref.ID, Path: path.Join(folder, name+".md"), Data: data}, records.Actor{Person: s.PersonID, Agent: s.AgentID})
		if errors.Is(err, records.ErrPathExists) {
			continue
		}
		return err
	}
}
