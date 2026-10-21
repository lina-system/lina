// SPDX-License-Identifier: Apache-2.0
package results

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto/sha1" // Git object format, not authorization.
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/thisisjun786/lina/internal/core/assets"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/kit/result"
)

var ErrRecordsChanged = errors.New("records_changed")

// ResolvePlace is a trusted lookup of a container id to its fixed PARA folder.
// It must check the person's ownership. Model input never supplies a path.
type ResolvePlace func(context.Context, result.Scope, result.Destination) (string, error)

// Records writes initial saved records in the shared records repository. The
// future records editor owns later versions. Core must hold its writer lock.
type Records struct {
	db      Canon
	root    *os.Root
	assets  *assets.Store
	resolve ResolvePlace
}

var recordsMu sync.Mutex

func OpenRecords(stateRoot string, db Canon, a *assets.Store, resolve ResolvePlace) (*Records, error) {
	if db == nil {
		return nil, result.ErrMalformed
	}
	root, err := os.OpenRoot(stateRoot)
	if err != nil {
		return nil, err
	}
	if err = privateDirectories(root, "versions/records"); err != nil {
		root.Close()
		return nil, err
	}
	repo, err := root.OpenRoot("versions/records")
	root.Close()
	if err != nil {
		return nil, err
	}
	return &Records{root: repo, db: db, assets: a, resolve: resolve}, nil
}
func (r *Records) Close() error { return r.root.Close() }
func privateDirectories(root *os.Root, name string) error {
	if !fs.ValidPath(name) {
		return result.ErrMalformed
	}
	parent := "."
	for _, part := range strings.Split(name, "/") {
		current := path.Join(parent, part)
		if err := root.Mkdir(current, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		info, err := root.Lstat(current)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode().Perm() != 0700 {
			return result.ErrMalformed
		}
		if err := syncDirectory(root, parent); err != nil {
			return err
		}
		parent = current
	}
	return nil
}
func syncDirectory(root *os.Root, name string) error {
	f, err := root.Open(name)
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close())
}
func regular(root *os.Root, name string) ([]byte, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, ErrRecordsChanged
	}
	return root.ReadFile(name)
}
func (r *Records) atomic(name string, data []byte) error {
	if err := privateDirectories(r.root, path.Dir(name)); err != nil {
		return err
	}
	tmp := path.Join(path.Dir(name), ".result-"+newID())
	f, err := r.root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer r.root.Remove(tmp)
	n, err := f.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	if err = r.root.Rename(tmp, name); err != nil {
		return err
	}
	return syncDirectory(r.root, path.Dir(name))
}
func (r *Records) initialize() error {
	if info, err := r.root.Lstat(".git"); err == nil {
		if !info.IsDir() {
			return ErrRecordsChanged
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := privateDirectories(r.root, ".git/objects"); err != nil {
		return err
	}
	if err := r.atomic(".git/config", []byte("[core]\nrepositoryformatversion = 0\nbare = false\nfilemode = false\n")); err != nil {
		return err
	}
	return r.atomic(".git/HEAD", []byte("ref: refs/heads/records\n"))
}
func objectID(kind string, data []byte) (string, []byte) {
	content := append([]byte(fmt.Sprintf("%s %d\x00", kind, len(data))), data...)
	sum := sha1.Sum(content)
	return hex.EncodeToString(sum[:]), content
}
func (r *Records) object(kind string, data []byte) (string, error) {
	hash, content := objectID(kind, data)
	name := ".git/objects/" + hash[:2] + "/" + hash[2:]
	if _, err := r.root.Lstat(name); err == nil {
		old, err := r.readObject(hash, kind)
		if err != nil {
			return "", err
		}
		if !bytes.Equal(old, data) {
			return "", ErrRecordsChanged
		}
		return hash, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	var compressed bytes.Buffer
	w := zlib.NewWriter(&compressed)
	if _, err := w.Write(content); err != nil {
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", err
	}
	return hash, r.atomic(name, compressed.Bytes())
}
func (r *Records) readObject(hash, kind string) ([]byte, error) {
	h, err := hex.DecodeString(hash)
	if err != nil || len(h) != 20 || hex.EncodeToString(h) != hash {
		return nil, ErrRecordsChanged
	}
	raw, err := regular(r.root, ".git/objects/"+hash[:2]+"/"+hash[2:])
	if err != nil {
		return nil, err
	}
	z, err := zlib.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	defer z.Close()
	content, err := io.ReadAll(io.LimitReader(z, 32<<20))
	if err != nil {
		return nil, err
	}
	header, body, ok := bytes.Cut(content, []byte{0})
	expected, _ := objectID(kind, body)
	if !ok || string(header) != fmt.Sprintf("%s %d", kind, len(body)) || expected != hash {
		return nil, ErrRecordsChanged
	}
	return body, nil
}
func (r *Records) head() (string, map[string]string, error) {
	head, err := regular(r.root, ".git/HEAD")
	if err != nil || string(head) != "ref: refs/heads/records\n" {
		return "", nil, ErrRecordsChanged
	}
	ref, err := regular(r.root, ".git/refs/heads/records")
	if errors.Is(err, os.ErrNotExist) {
		return "", map[string]string{}, nil
	}
	if err != nil {
		return "", nil, err
	}
	hash := strings.TrimSpace(string(ref))
	commit, err := r.readObject(hash, "commit")
	if err != nil {
		return "", nil, err
	}
	line, _, ok := strings.Cut(string(commit), "\n")
	if !ok || !strings.HasPrefix(line, "tree ") {
		return "", nil, ErrRecordsChanged
	}
	files := map[string]string{}
	err = r.readTree(strings.TrimPrefix(line, "tree "), "", files, 0)
	return hash, files, err
}
func (r *Records) readTree(hash, prefix string, files map[string]string, depth int) error {
	if depth > 32 {
		return ErrRecordsChanged
	}
	data, err := r.readObject(hash, "tree")
	if err != nil {
		return err
	}
	for len(data) > 0 {
		header, rest, ok := bytes.Cut(data, []byte{0})
		if !ok || len(rest) < 20 {
			return ErrRecordsChanged
		}
		mode, name, ok := strings.Cut(string(header), " ")
		if !ok || name == "" || name == "." || name == ".." || strings.Contains(name, "/") {
			return ErrRecordsChanged
		}
		full := path.Join(prefix, name)
		id := hex.EncodeToString(rest[:20])
		data = rest[20:]
		switch mode {
		case "40000":
			if err := r.readTree(id, full, files, depth+1); err != nil {
				return err
			}
		case "100644":
			if _, ok := files[full]; ok {
				return ErrRecordsChanged
			}
			files[full] = id
		default:
			return ErrRecordsChanged
		}
	}
	return nil
}
func (r *Records) unchanged(files map[string]string) error {
	seen := map[string]bool{}
	err := fs.WalkDir(r.root.FS(), ".", func(name string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if name == ".git" {
			return fs.SkipDir
		}
		if e.IsDir() {
			return nil
		}
		hash, ok := files[name]
		if !ok || !e.Type().IsRegular() {
			return ErrRecordsChanged
		}
		data, err := regular(r.root, name)
		if err != nil {
			return err
		}
		actual, _ := objectID("blob", data)
		if actual != hash {
			return ErrRecordsChanged
		}
		seen[name] = true
		return nil
	})
	if err != nil {
		return err
	}
	if len(seen) != len(files) {
		return ErrRecordsChanged
	}
	return nil
}
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
func (r *Records) Save(ctx context.Context, s result.Scope, value result.Result, ref result.SavedRef) error {
	recordsMu.Lock()
	defer recordsMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if ref.ID == "" || ref.Version != 1 || value.ID == "" || value.Revision <= 0 || !validScope(s) {
		return result.ErrMalformed
	}
	if _, err := result.Validate(value.Type, value.Body, result.Limits{}); err != nil {
		return err
	}
	folder, err := r.folder(ctx, s, ref.Destination)
	if err != nil {
		return err
	}
	if err = r.initialize(); err != nil {
		return err
	}
	lock, err := r.root.OpenFile(".git/index.lock", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	lock.Close()
	defer r.root.Remove(".git/index.lock")
	parent, files, err := r.head()
	if err != nil {
		return err
	}
	var expected string
	if err := r.db.Read(ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT head FROM result_records_state WHERE singleton=1").Scan(&expected)
	}); err != nil {
		return err
	}
	if parent != expected {
		return ErrRecordsChanged
	}

	if err = r.unchanged(files); err != nil {
		return err
	}
	data := recordBytes(s, value, ref)
	for name := range files {
		old, err := regular(r.root, name)
		if err != nil {
			return err
		}
		id, _ := json.Marshal(ref.ID)
		if bytes.HasPrefix(old, []byte("---\nid: "+string(id)+"\n")) {
			if bytes.Equal(old, data) {
				return nil
			}
			return ErrRecordsChanged
		}
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
		data = recordBytes(s, value, ref)
	}
	stem := slug(value.Title)
	name := path.Join(folder, stem+".md")
	for n := 2; files[name] != ""; n++ {
		name = path.Join(folder, stem+"-"+strconv.Itoa(n)+".md")
	}
	blob, err := r.object("blob", data)
	if err != nil {
		return err
	}
	files[name] = blob
	tree, err := r.tree(files)
	if err != nil {
		return err
	}
	stamp := strconv.FormatInt(time.Now().Unix(), 10)
	message := "tree " + tree + "\n"
	if parent != "" {
		message += "parent " + parent + "\n"
	}
	person, _ := json.Marshal(s.PersonID)
	agent, _ := json.Marshal(s.AgentID)
	message += "author LINA <lina@example.invalid> " + stamp + " +0000\ncommitter LINA <lina@example.invalid> " + stamp + " +0000\n\nSave result\nPerson: " + string(person) + "\nAgent: " + string(agent) + "\n"
	commit, err := r.object("commit", []byte(message))
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err = r.atomic(name, data); err != nil {
		return err
	}
	if err = r.atomic(".git/index", gitIndex(files)); err != nil {
		return err
	}
	if err := r.atomic(".git/refs/heads/records", []byte(commit+"\n")); err != nil {
		return err
	}
	return r.db.Write(ctx, func(tx *canon.Tx) error {
		changed, err := tx.ExecContext(ctx, "UPDATE result_records_state SET head=? WHERE singleton=1 AND head=?", commit, parent)
		if err != nil {
			return err
		}
		n, err := changed.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrRecordsChanged
		}
		return nil
	})
}
func (r *Records) tree(files map[string]string) (string, error) {
	type entry struct{ name, mode, hash string }
	var entries []entry
	dirs := map[string]map[string]string{}
	for name, hash := range files {
		dir, rest, nested := strings.Cut(name, "/")
		if !nested {
			entries = append(entries, entry{name, "100644", hash})
			continue
		}
		if dirs[dir] == nil {
			dirs[dir] = map[string]string{}
		}
		dirs[dir][rest] = hash
	}
	for dir, children := range dirs {
		hash, err := r.tree(children)
		if err != nil {
			return "", err
		}
		entries = append(entries, entry{dir, "40000", hash})
	}
	sort.Slice(entries, func(i, j int) bool {
		a, b := entries[i].name, entries[j].name
		if entries[i].mode == "40000" {
			a += "/"
		}
		if entries[j].mode == "40000" {
			b += "/"
		}
		return a < b
	})
	var data []byte
	for _, e := range entries {
		hash, _ := hex.DecodeString(e.hash)
		data = append(data, []byte(e.mode+" "+e.name+"\x00")...)
		data = append(data, hash...)
	}
	return r.object("tree", data)
}
func gitIndex(files map[string]string) []byte {
	var b bytes.Buffer
	b.WriteString("DIRC")
	binary.Write(&b, binary.BigEndian, uint32(2))
	binary.Write(&b, binary.BigEndian, uint32(len(files)))
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		start := b.Len()
		for i := 0; i < 10; i++ {
			v := uint32(0)
			if i == 6 {
				v = 0100644
			}
			binary.Write(&b, binary.BigEndian, v)
		}
		hash, _ := hex.DecodeString(files[name])
		b.Write(hash)
		binary.Write(&b, binary.BigEndian, uint16(min(len(name), 0xfff)))
		b.WriteString(name)
		b.WriteByte(0)
		for (b.Len()-start)%8 != 0 {
			b.WriteByte(0)
		}
	}
	sum := sha1.Sum(b.Bytes())
	b.Write(sum[:])
	return b.Bytes()
}
