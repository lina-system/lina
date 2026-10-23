// SPDX-License-Identifier: Apache-2.0
package miniapps

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/kit/result"
)

// Reads answers a named Core read for one app. The service has already checked
// that the app declared the name and that the person owns the app.
type Reads interface {
	Read(ctx context.Context, person, agent, name string, arguments json.RawMessage) (json.RawMessage, error)
}

// Effects runs one declared request as the owner agent's effect. It returns a
// waiting result when the person must answer an approval; it never answers one.
type Effects interface {
	Run(ctx context.Context, request EffectRequest) (EffectResult, error)
}
type EffectRequest struct {
	Person, Agent, AppID, AppName, RequestID, Name string
	Arguments                                      json.RawMessage
}
type EffectResult struct {
	State   string `json:"state"` // applied, waiting or refused
	Summary string `json:"summary,omitempty"`
	Code    string `json:"code,omitempty"`
}

// Fetcher fetches one of the hosts an app declared, under the web tools rules.
// It receives the whole list so a redirect cannot leave it.
type Fetcher interface {
	Fetch(ctx context.Context, hosts []string, rawURL string) ([]byte, string, error)
}

// App is the registry record. The token hash is never exposed in a listing.
type App struct {
	ID              string    `json:"id"`
	Person          string    `json:"person"`
	Agent           string    `json:"agent"`
	Name            string    `json:"name"`
	Request         string    `json:"request"`
	Manifest        Manifest  `json:"manifest"`
	Revision        int64     `json:"revision"`
	AllowedRevision int64     `json:"allowed_revision"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
	Removed         bool      `json:"removed"`

	tokenHash  string
	cookieName string
	storeShape []string
}

func (a App) Allowed() bool {
	return !a.Removed && a.Revision > 0 && a.AllowedRevision == a.Revision
}
func (a App) Cookie() string { return a.cookieName }

// Revision is one checked set of app files with the request that produced it.
type Revision struct {
	AppID         string    `json:"app"`
	Number        int64     `json:"revision"`
	Request       string    `json:"request"`
	Summary       string    `json:"summary"`
	Files         []File    `json:"files"`
	StoreSnapshot bool      `json:"store_snapshot,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

type Change struct {
	App      App      `json:"app"`
	Revision Revision `json:"revision"`
	Changed  bool     `json:"changed"`
	Widened  bool     `json:"widened"`
}

type CreateRequest struct {
	Name      string
	Request   string
	Checklist []result.MockItem
	Manifest  Manifest
	Files     map[string][]byte
	// StoreShape names the top-level keys the app's store will use. A change
	// of shape snapshots the current store before it is served.
	StoreShape []string
	// Key makes a retried create return the existing app.
	Key string
}

type ChangeRequest struct {
	Request    string
	Checklist  []result.MockItem
	Manifest   Manifest
	Files      map[string][]byte
	StoreShape []string
}

// CheckFunc runs the executable pre-live checks. It must use the same rules as
// drawn mocks: size, tokens only, no remote address, a clean draw at phone and
// desktop widths, and every checklist item present.
type CheckFunc func(ctx context.Context, files map[string][]byte, manifest Manifest, checklist []result.MockItem) (CheckResult, error)

type CheckResult struct {
	Passed bool
	Checks []result.MockCheck
	Reason string
	// Capture is a desktop PNG of the rendered revision, kept as evidence.
	Capture []byte
}

// Canon is the storage seam shared with the other Core services.
type Canon interface {
	Write(context.Context, func(*canon.Tx) error) error
	Read(context.Context, func(*canon.Tx) error) error
}

type Config struct {
	DB      Canon
	Root    string
	Now     func() time.Time
	Check   CheckFunc
	Reads   Reads
	Effects Effects
	Fetcher Fetcher
	// OnRemove closes a live origin before its files move to the trash.
	OnRemove func(appID string)
	Random   io.Reader
}

type Service struct {
	cfg Config
	mu  chan struct{}
}

func New(cfg Config) (*Service, error) {
	if cfg.DB == nil || cfg.Root == "" || cfg.Reads == nil || cfg.Effects == nil {
		return nil, ErrUnavailable
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Random == nil {
		cfg.Random = rand.Reader
	}
	return &Service{cfg: cfg, mu: make(chan struct{}, 1)}, nil
}

func (s *Service) now() time.Time { return s.cfg.Now() }

// SetOnRemove installs the hook that closes a live origin before an app's files
// move to the trash. It is set after the host exists.
func (s *Service) SetOnRemove(fn func(appID string)) {
	s.cfg.OnRemove = fn
}

func (s *Service) lock(ctx context.Context) error {
	select {
	case s.mu <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (s *Service) unlock() { <-s.mu }

func (s *Service) random(n int) ([]byte, error) {
	out := make([]byte, n)
	if _, err := io.ReadFull(s.cfg.Random, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Service) newAppID() (string, error) {
	raw, err := s.random(8)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}
func (s *Service) newToken() (token, hash string, err error) {
	raw, err := s.random(32)
	if err != nil {
		return "", "", err
	}
	token = hex.EncodeToString(raw)
	sum := sha256.Sum256([]byte(token))
	return token, hex.EncodeToString(sum[:]), nil
}
func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
func cookieName(id string) string { return "lina_app_" + id }

func scanApp(row interface{ Scan(...any) error }) (App, error) {
	var a App
	var manifest, shape []byte
	var created, updated time.Time
	err := row.Scan(&a.ID, &a.Person, &a.Agent, &a.Name, &a.Request, &manifest, &a.Revision, &a.AllowedRevision, &a.tokenHash, &a.cookieName, &created, &updated, &a.Removed, &shape)
	if errors.Is(err, sql.ErrNoRows) {
		return App{}, ErrNotFound
	}
	if err != nil {
		return App{}, err
	}
	if err = json.Unmarshal(manifest, &a.Manifest); err != nil {
		return App{}, err
	}
	if err = json.Unmarshal(shape, &a.storeShape); err != nil {
		return App{}, err
	}
	a.CreatedAt = created.UTC()
	a.UpdatedAt = updated.UTC()
	return a, nil
}

const appColumns = "id,person,agent,name,request,manifest,revision,allowed_revision,token_hash,cookie_name,created_at,updated_at,removed,store_shape"

func (s *Service) load(ctx context.Context, person, appID string) (App, error) {
	var a App
	err := s.cfg.DB.Read(ctx, func(tx *canon.Tx) error {
		var err error
		a, err = scanApp(tx.QueryRowContext(ctx, "SELECT "+appColumns+" FROM mini_app WHERE id=? AND person=?", appID, person))
		return err
	})
	if err != nil {
		return App{}, err
	}
	if a.Removed {
		return App{}, ErrNotFound
	}
	return a, nil
}

// Get returns one app for its owner.
func (s *Service) Get(ctx context.Context, person, appID string) (App, error) {
	return s.load(ctx, person, appID)
}

// List returns every app the person owns, newest first.
func (s *Service) List(ctx context.Context, person string) ([]App, error) {
	var apps []App
	err := s.cfg.DB.Read(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT "+appColumns+" FROM mini_app WHERE person=? AND removed=0 ORDER BY created_at DESC, name", person)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			app, err := scanApp(rows)
			if err != nil {
				return err
			}
			apps = append(apps, app)
		}
		return rows.Err()
	})
	return apps, err
}

// Create records the first checked revision. It is not allowed until the person
// allows that revision; the first open shows the manifest for that decision.
func (s *Service) Create(ctx context.Context, person, agent string, req CreateRequest) (Change, error) {
	if err := s.lock(ctx); err != nil {
		return Change{}, err
	}
	defer s.unlock()
	if person == "" || agent == "" || req.Request == "" || req.Name == "" || result.ValidateMockChecklist(req.Checklist) != nil {
		return Change{}, ErrInvalid
	}
	manifest := cloneManifest(req.Manifest)
	manifest.Name = req.Name
	if err := ValidateManifest(manifest); err != nil {
		return Change{}, err
	}
	if err := ValidateFiles(req.Files); err != nil {
		return Change{}, err
	}
	if req.Key != "" {
		if existing, err := s.findCreateKey(ctx, person, req.Key); err == nil {
			return s.changeOf(ctx, existing)
		} else if !errors.Is(err, ErrNotFound) {
			return Change{}, err
		}
	}
	checked, err := s.check(ctx, req.Files, manifest, req.Checklist)
	if err != nil {
		return Change{}, err
	}
	if !checked.Passed {
		return Change{}, checkFailure(checked)
	}
	id, err := s.newAppID()
	if err != nil {
		return Change{}, err
	}
	_, hash, err := s.newToken()
	if err != nil {
		return Change{}, err
	}
	if err = s.writeRevision(id, 1, req.Files); err != nil {
		return Change{}, err
	}
	if err = s.writeCapture(id, 1, checked.Capture); err != nil {
		_ = s.removeAppDir(id)
		return Change{}, err
	}
	if err = s.writeRecipe(id, 1, manifest, req.Checklist, sortedUnique(req.StoreShape)); err != nil {
		_ = s.removeAppDir(id)
		return Change{}, err
	}
	now := s.now().UTC()
	infos := fileInfos(req.Files)
	encoded, _ := encodeFiles(infos)
	manifestRaw, _ := json.Marshal(manifest)
	shape, _ := json.Marshal(sortedUnique(req.StoreShape))
	app := App{ID: id, Person: person, Agent: agent, Name: req.Name, Request: req.Request, Manifest: manifest, Revision: 1, AllowedRevision: 0, CreatedAt: now, UpdatedAt: now, Removed: false, tokenHash: hash, cookieName: cookieName(id), storeShape: sortedUnique(req.StoreShape)}
	err = s.cfg.DB.Write(ctx, func(tx *canon.Tx) error {
		if _, err := tx.ExecContext(ctx, "INSERT INTO mini_app("+appColumns+",create_key) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)", app.ID, app.Person, app.Agent, app.Name, app.Request, manifestRaw, 1, 0, hash, app.cookieName, now, now, false, shape, req.Key); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "INSERT INTO mini_app_revision(app,revision,request,summary,files,store_snapshot,created_at) VALUES(?,?,?,?,?,?,?)", id, 1, req.Request, "", encoded, false, now)
		return err
	})
	if err != nil {
		_ = s.removeAppDir(id)
		return Change{}, err
	}
	return Change{App: app, Revision: Revision{AppID: id, Number: 1, Request: req.Request, Files: infos, CreatedAt: now}, Changed: true}, nil
}

func (s *Service) findCreateKey(ctx context.Context, person, key string) (string, error) {
	var id string
	err := s.cfg.DB.Read(ctx, func(tx *canon.Tx) error {
		err := tx.QueryRowContext(ctx, "SELECT id FROM mini_app WHERE person=? AND create_key=? AND create_key<>''", person, key).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	})
	return id, err
}

func (s *Service) changeOf(ctx context.Context, appID string) (Change, error) {
	var app App
	err := s.cfg.DB.Read(ctx, func(tx *canon.Tx) error {
		var err error
		app, err = scanApp(tx.QueryRowContext(ctx, "SELECT "+appColumns+" FROM mini_app WHERE id=?", appID))
		return err
	})
	if err != nil {
		return Change{}, err
	}
	rev, err := s.revision(ctx, appID, app.Revision)
	if err != nil {
		return Change{}, err
	}
	return Change{App: app, Revision: rev, Changed: true}, nil
}

func sortedUnique(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
}

func (s *Service) check(ctx context.Context, files map[string][]byte, manifest Manifest, checklist []result.MockItem) (CheckResult, error) {
	if result.ValidateMockChecklist(checklist) != nil {
		return CheckResult{}, ErrInvalid
	}
	if err := ValidateFiles(files); err != nil {
		return CheckResult{}, err
	}
	if s.cfg.Check == nil {
		return CheckResult{}, ErrUnavailable
	}
	checked, err := s.cfg.Check(ctx, files, manifest, checklist)
	if err != nil {
		return CheckResult{}, err
	}
	if checked.Passed {
		for _, check := range checked.Checks {
			if !check.Passed {
				checked.Passed = false
				if checked.Reason == "" {
					checked.Reason = check.ID
				}
				break
			}
		}
	}
	return checked, nil
}

// checkFailure names the dimension that stopped a revision, so the worker and
// the person can see what to fix. errors.Is still matches ErrCheck.
func checkFailure(checked CheckResult) error {
	if checked.Reason == "" {
		return ErrCheck
	}
	return fmt.Errorf("%w: %s", ErrCheck, checked.Reason)
}

func (s *Service) revision(ctx context.Context, appID string, number int64) (Revision, error) {
	var rev Revision
	var files []byte
	var created time.Time
	err := s.cfg.DB.Read(ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT app,revision,request,summary,files,store_snapshot,created_at FROM mini_app_revision WHERE app=? AND revision=?", appID, number).Scan(&rev.AppID, &rev.Number, &rev.Request, &rev.Summary, &files, &rev.StoreSnapshot, &created)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return Revision{}, ErrNotFound
	}
	if err != nil {
		return Revision{}, err
	}
	rev.CreatedAt = created.UTC()
	infos, err := decodeFiles(files)
	if err != nil {
		return Revision{}, err
	}
	rev.Files = infos
	return rev, nil
}

// History lists revisions newest first.
func (s *Service) History(ctx context.Context, person, appID string) ([]Revision, error) {
	if _, err := s.load(ctx, person, appID); err != nil {
		return nil, err
	}
	var out []Revision
	err := s.cfg.DB.Read(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT app,revision,request,summary,files,store_snapshot,created_at FROM mini_app_revision WHERE app=? ORDER BY revision DESC", appID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var rev Revision
			var files []byte
			var created time.Time
			if err := rows.Scan(&rev.AppID, &rev.Number, &rev.Request, &rev.Summary, &files, &rev.StoreSnapshot, &created); err != nil {
				return err
			}
			rev.CreatedAt = created.UTC()
			if rev.Files, err = decodeFiles(files); err != nil {
				return err
			}
			out = append(out, rev)
		}
		return rows.Err()
	})
	return out, err
}

// Change records a checked revision when the bytes really differ. A no-op
// returns Changed=false and records nothing.
func (s *Service) Change(ctx context.Context, person, appID string, req ChangeRequest) (Change, error) {
	if err := s.lock(ctx); err != nil {
		return Change{}, err
	}
	defer s.unlock()
	app, err := s.load(ctx, person, appID)
	if err != nil {
		return Change{}, err
	}
	if req.Request == "" || result.ValidateMockChecklist(req.Checklist) != nil {
		return Change{}, ErrInvalid
	}
	manifest := cloneManifest(req.Manifest)
	manifest.Name = app.Name
	if err := ValidateManifest(manifest); err != nil {
		return Change{}, err
	}
	if err := ValidateFiles(req.Files); err != nil {
		return Change{}, err
	}
	if app.Removed {
		return Change{}, ErrNotFound
	}
	current, err := s.readRevision(appID, app.Revision)
	if err != nil {
		return Change{}, err
	}
	shape := sortedUnique(req.StoreShape)
	summary, changed := DiffSummary(current, req.Files)
	// Declarations are part of the revision: narrowing or widening them without
	// touching the files still has to be checked, recorded and re-asked.
	if !changed && sameManifest(app.Manifest, manifest) && sameStrings(shape, app.storeShape) {
		return Change{App: app, Changed: false}, nil
	}
	if summary == "" {
		summary = "declarations changed"
	}
	checked, err := s.check(ctx, req.Files, manifest, req.Checklist)
	if err != nil {
		return Change{}, err
	}
	if !checked.Passed {
		return Change{}, checkFailure(checked)
	}
	next := app.Revision + 1
	snapshot := !sameStrings(shape, app.storeShape)
	if snapshot {
		if err := s.snapshotStore(app); err != nil {
			return Change{}, err
		}
	}
	if err := s.writeRevision(appID, next, req.Files); err != nil {
		return Change{}, err
	}
	if err := s.writeCapture(appID, next, checked.Capture); err != nil {
		return Change{}, err
	}
	if err := s.writeRecipe(appID, next, manifest, req.Checklist, shape); err != nil {
		return Change{}, err
	}
	widened := app.Manifest.Widens(manifest)
	allowed, hash, err := s.carryConsent(app, next, manifest)
	if err != nil {
		return Change{}, err
	}
	now := s.now().UTC()
	infos := fileInfos(req.Files)
	encoded, _ := encodeFiles(infos)
	manifestRaw, _ := json.Marshal(manifest)
	shapeRaw, _ := json.Marshal(shape)
	err = s.cfg.DB.Write(ctx, func(tx *canon.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO mini_app_revision(app,revision,request,summary,files,store_snapshot,created_at) VALUES(?,?,?,?,?,?,?)", appID, next, req.Request, summary, encoded, snapshot, now)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE mini_app SET manifest=?,revision=?,allowed_revision=?,token_hash=?,store_shape=?,updated_at=? WHERE id=?", manifestRaw, next, allowed, hash, shapeRaw, now, appID); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO mini_app_change(id,app,revision,request,summary,created_at) VALUES(?,?,?,?,?,?)", appID+":"+now.Format(time.RFC3339Nano), appID, next, req.Request, summary, now)
		return err
	})
	if err != nil {
		_ = s.removeRevision(appID, next)
		return Change{}, err
	}
	app.Revision, app.AllowedRevision, app.Manifest, app.UpdatedAt, app.tokenHash, app.storeShape = next, allowed, manifest, now, hash, shape
	return Change{App: app, Revision: Revision{AppID: appID, Number: next, Request: req.Request, Summary: summary, Files: infos, StoreSnapshot: snapshot, CreatedAt: now}, Changed: true, Widened: widened}, nil
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// carryConsent decides what a new revision inherits from the person's standing
// choice. Keeping the declarations keeps an allowed app open; widening or
// narrowing them waits for a new allow and rotates the token, so an older cookie
// stops working. An app the person never allowed stays unallowed.
func (s *Service) carryConsent(app App, next int64, manifest Manifest) (int64, string, error) {
	if !app.Manifest.Widens(manifest) && !app.Manifest.Narrows(manifest) {
		if app.Allowed() {
			return next, app.tokenHash, nil
		}
		return 0, app.tokenHash, nil
	}
	_, hash, err := s.newToken()
	if err != nil {
		return 0, "", err
	}
	return 0, hash, nil
}

// sameManifest reports whether two manifests declare the same scope. The name
// is fixed by the app, so it never counts as a change.
func sameManifest(a, b Manifest) bool {
	return a.Schedule == b.Schedule && sameStrings(a.Reads, b.Reads) &&
		sameStrings(a.Requests, b.Requests) && sameStrings(a.Hosts, b.Hosts)
}

// Allow records the person's decision for the current revision and rotates the
// token, so an older code or cookie cannot keep the newly allowed scope.
func (s *Service) Allow(ctx context.Context, person, appID string, revision int64) (App, error) {
	if err := s.lock(ctx); err != nil {
		return App{}, err
	}
	defer s.unlock()
	app, err := s.load(ctx, person, appID)
	if err != nil {
		return App{}, err
	}
	if revision != app.Revision {
		return App{}, ErrStale
	}
	if app.Allowed() {
		return app, nil
	}
	if _, err := s.readRevision(appID, app.Revision); err != nil {
		return App{}, err
	}
	_, hash, err := s.newToken()
	if err != nil {
		return App{}, err
	}
	now := s.now().UTC()
	if err := s.cfg.DB.Write(ctx, func(tx *canon.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE mini_app SET allowed_revision=?,token_hash=?,updated_at=? WHERE id=?", app.Revision, hash, now, appID)
		return err
	}); err != nil {
		return App{}, err
	}
	app.AllowedRevision, app.UpdatedAt, app.tokenHash = app.Revision, now, hash
	return app, nil
}

// Deny leaves the current revision unusable. The files stay in history and can
// be restored as a new revision.
func (s *Service) Deny(ctx context.Context, person, appID string, revision int64) (App, error) {
	if err := s.lock(ctx); err != nil {
		return App{}, err
	}
	defer s.unlock()
	app, err := s.load(ctx, person, appID)
	if err != nil {
		return App{}, err
	}
	if revision != app.Revision {
		return App{}, ErrStale
	}
	_, hash, err := s.newToken()
	if err != nil {
		return App{}, err
	}
	now := s.now().UTC()
	if err := s.cfg.DB.Write(ctx, func(tx *canon.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE mini_app SET allowed_revision=0,token_hash=?,updated_at=? WHERE id=?", hash, now, appID)
		return err
	}); err != nil {
		return App{}, err
	}
	app.AllowedRevision, app.UpdatedAt, app.tokenHash = 0, now, hash
	return app, nil
}

// Keep is the person's acknowledgement of a shown revision. Revisions are
// already durable, so Keep only returns the current state.
func (s *Service) Keep(ctx context.Context, person, appID string) (App, error) {
	return s.load(ctx, person, appID)
}

// Undo restores the previous revision's files and manifest as a new revision.
// It never touches the store.
func (s *Service) Undo(ctx context.Context, person, appID string) (Change, error) {
	app, err := s.load(ctx, person, appID)
	if err != nil {
		return Change{}, err
	}
	if app.Revision < 2 {
		return Change{}, ErrInvalid
	}
	return s.restore(ctx, person, appID, app.Revision-1, "undo revision "+itoa(app.Revision))
}

// Restore makes any earlier revision current again as a new revision.
func (s *Service) Restore(ctx context.Context, person, appID string, revision int64) (Change, error) {
	return s.restore(ctx, person, appID, revision, "restore revision "+itoa(revision))
}

func (s *Service) restore(ctx context.Context, person, appID string, revision int64, request string) (Change, error) {
	if err := s.lock(ctx); err != nil {
		return Change{}, err
	}
	defer s.unlock()
	app, err := s.load(ctx, person, appID)
	if err != nil {
		return Change{}, err
	}
	if revision < 1 || revision >= app.Revision {
		return Change{}, ErrInvalid
	}
	files, err := s.readRevision(appID, revision)
	if err != nil {
		return Change{}, err
	}
	manifest := app.Manifest
	var recipe struct {
		Manifest  Manifest          `json:"manifest"`
		Checklist []result.MockItem `json:"checklist"`
		Shape     []string          `json:"shape"`
	}
	raw, err := s.readRecipe(appID, revision)
	if err == nil {
		if json.Unmarshal(raw, &recipe) != nil {
			return Change{}, ErrInvalid
		}
	}
	if recipe.Manifest.Name == "" {
		return Change{}, ErrInvalid
	}
	manifest = recipe.Manifest
	manifest.Name = app.Name
	if err := ValidateManifest(manifest); err != nil {
		return Change{}, err
	}
	if result.ValidateMockChecklist(recipe.Checklist) != nil {
		return Change{}, ErrInvalid
	}
	checked, err := s.check(ctx, files, manifest, recipe.Checklist)
	if err != nil {
		return Change{}, err
	}
	if !checked.Passed {
		return Change{}, checkFailure(checked)
	}
	next := app.Revision + 1
	shape := sortedUnique(recipe.Shape)
	snapshot := !sameStrings(shape, app.storeShape)
	if snapshot {
		if err := s.snapshotStore(app); err != nil {
			return Change{}, err
		}
	}
	if err := s.writeRevision(appID, next, files); err != nil {
		return Change{}, err
	}
	if err := s.writeRecipe(appID, next, manifest, recipe.Checklist, shape); err != nil {
		return Change{}, err
	}
	allowed, hash, err := s.carryConsent(app, next, manifest)
	if err != nil {
		return Change{}, err
	}
	now := s.now().UTC()
	manifestRaw, _ := json.Marshal(manifest)
	shapeRaw, _ := json.Marshal(shape)
	infos := fileInfos(files)
	encoded, _ := encodeFiles(infos)
	summary := "restored revision " + itoa(revision)
	err = s.cfg.DB.Write(ctx, func(tx *canon.Tx) error {
		if _, err := tx.ExecContext(ctx, "INSERT INTO mini_app_revision(app,revision,request,summary,files,store_snapshot,created_at) VALUES(?,?,?,?,?,?,?)", appID, next, request, summary, encoded, false, now); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "UPDATE mini_app SET manifest=?,revision=?,allowed_revision=?,token_hash=?,store_shape=?,updated_at=? WHERE id=?", manifestRaw, next, allowed, hash, shapeRaw, now, appID)
		return err
	})
	if err != nil {
		_ = s.removeRevision(appID, next)
		return Change{}, err
	}
	app.Revision, app.AllowedRevision, app.Manifest, app.UpdatedAt, app.tokenHash = next, allowed, manifest, now, hash
	return Change{App: app, Revision: Revision{AppID: appID, Number: next, Request: request, Summary: summary, Files: infos, CreatedAt: now}, Changed: true}, nil
}

func itoa(n int64) string {
	raw, _ := json.Marshal(n)
	return string(raw)
}

// Remove revokes the token at once and moves the files and store to the trash.
func (s *Service) Remove(ctx context.Context, person, appID string) error {
	if err := s.lock(ctx); err != nil {
		return err
	}
	defer s.unlock()
	app, err := s.load(ctx, person, appID)
	if err != nil {
		return err
	}
	if s.cfg.OnRemove != nil {
		s.cfg.OnRemove(app.ID)
	}
	now := s.now().UTC()
	if err := s.cfg.DB.Write(ctx, func(tx *canon.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE mini_app SET removed=1,token_hash='',allowed_revision=0,updated_at=? WHERE id=?", now, app.ID)
		return err
	}); err != nil {
		return err
	}
	return s.moveToTrash(appID)
}

// MintToken issues the app's current token. The plaintext leaves the service
// only into an HttpOnly cookie; the page script never receives it.
func (s *Service) MintToken(ctx context.Context, appID string) (string, error) {
	if err := s.lock(ctx); err != nil {
		return "", err
	}
	defer s.unlock()
	app, err := s.loadAnyPerson(ctx, appID)
	if err != nil {
		return "", err
	}
	if !app.Allowed() {
		return "", ErrForbidden
	}
	token, hash, err := s.newToken()
	if err != nil {
		return "", err
	}
	now := s.now().UTC()
	if err := s.cfg.DB.Write(ctx, func(tx *canon.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE mini_app SET token_hash=?,updated_at=? WHERE id=?", hash, now, appID)
		return err
	}); err != nil {
		return "", err
	}
	return token, nil
}

// Authorize admits a page request that carries the app's own token. Cookies are
// scoped by host, not port, so the caller passes only that app's cookie and a
// token for another app cannot match this hash.
func (s *Service) Authorize(ctx context.Context, appID, token string) (App, error) {
	app, err := s.loadAnyPerson(ctx, appID)
	if err != nil {
		// A removed or unknown app is refused the same way, so a page cannot
		// tell an app that never existed from one the person removed.
		return App{}, ErrForbidden
	}
	if !app.Allowed() || app.tokenHash == "" || token == "" {
		return App{}, ErrForbidden
	}
	if subtle.ConstantTimeCompare([]byte(tokenHash(token)), []byte(app.tokenHash)) != 1 {
		return App{}, ErrForbidden
	}
	return app, nil
}

func (s *Service) loadAnyPerson(ctx context.Context, appID string) (App, error) {
	var app App
	err := s.cfg.DB.Read(ctx, func(tx *canon.Tx) error {
		var err error
		app, err = scanApp(tx.QueryRowContext(ctx, "SELECT "+appColumns+" FROM mini_app WHERE id=?", appID))
		return err
	})
	if errors.Is(err, sql.ErrNoRows) {
		return App{}, ErrNotFound
	}
	if err != nil {
		return App{}, err
	}
	if app.Removed {
		return App{}, ErrNotFound
	}
	return app, nil
}

// Read serves one declared Core read for an allowed app.
func (s *Service) Read(ctx context.Context, appID, token, name string, arguments json.RawMessage) (json.RawMessage, error) {
	app, err := s.Authorize(ctx, appID, token)
	if err != nil {
		return nil, err
	}
	if !app.Manifest.HasRead(name) {
		s.event(ctx, app.ID, "read", "refused "+name)
		return nil, ErrForbidden
	}
	out, err := s.cfg.Reads.Read(ctx, app.Person, app.Agent, name, arguments)
	if err != nil {
		s.event(ctx, app.ID, "read", "failed "+name)
		return nil, err
	}
	return out, nil
}

// Request runs one declared request as the owner agent's effect. It never
// answers an approval and never treats the app's call as the person's decision.
func (s *Service) Request(ctx context.Context, appID, token, requestID, name string, arguments json.RawMessage) (EffectResult, error) {
	app, err := s.Authorize(ctx, appID, token)
	if err != nil {
		return EffectResult{}, err
	}
	if !app.Manifest.HasRequest(name) {
		s.event(ctx, app.ID, "request", "refused "+name)
		return EffectResult{}, ErrForbidden
	}
	if requestID == "" || len(requestID) > 128 || len(arguments) > 64<<10 {
		return EffectResult{}, ErrInvalid
	}
	out, err := s.cfg.Effects.Run(ctx, EffectRequest{Person: app.Person, Agent: app.Agent, AppID: app.ID, AppName: app.Name, RequestID: requestID, Name: name, Arguments: arguments})
	if err != nil {
		s.event(ctx, app.ID, "request", "failed "+name)
		return EffectResult{}, err
	}
	if out.State != "applied" && out.State != "waiting" && out.State != "refused" {
		out.State = "refused"
	}
	s.event(ctx, app.ID, "request", out.State+" "+name)
	return out, nil
}

// Fetch fetches a declared host for the app under the web tools rules.
func (s *Service) Fetch(ctx context.Context, appID, token, rawURL string) ([]byte, string, error) {
	app, err := s.Authorize(ctx, appID, token)
	if err != nil {
		return nil, "", err
	}
	if s.cfg.Fetcher == nil || !app.Manifest.HasHost(rawURL) {
		s.event(ctx, app.ID, "fetch", "refused "+rawURL)
		return nil, "", ErrForbidden
	}
	body, media, err := s.cfg.Fetcher.Fetch(ctx, app.Manifest.Hosts, rawURL)
	if err != nil {
		return nil, "", err
	}
	return body, media, nil
}

func (s *Service) event(ctx context.Context, appID, kind, detail string) {
	if len(detail) > 256 {
		detail = detail[:256]
	}
	now := s.now().UTC()
	_ = s.cfg.DB.Write(ctx, func(tx *canon.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO mini_app_event(id,app,kind,detail,created_at) VALUES(?,?,?,?,?)", appID+":"+now.Format(time.RFC3339Nano), appID, kind, detail, now)
		if err == nil {
			_, err = tx.ExecContext(ctx, "DELETE FROM mini_app_event WHERE app=? AND rowid NOT IN (SELECT rowid FROM mini_app_event WHERE app=? ORDER BY created_at DESC LIMIT 500)", appID, appID)
		}
		return err
	})
}

// Events returns the recent gateway events for one app.
func (s *Service) Events(ctx context.Context, person, appID string) ([]string, error) {
	if _, err := s.load(ctx, person, appID); err != nil {
		return nil, err
	}
	var out []string
	err := s.cfg.DB.Read(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT kind||' '||detail FROM mini_app_event WHERE app=? ORDER BY created_at DESC LIMIT 100", appID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				return err
			}
			out = append(out, line)
		}
		return rows.Err()
	})
	return out, err
}

func (s *Service) storePath(appID string) string {
	return filepath.Join(s.cfg.Root, "miniapps", appID, "store.json")
}

// Store returns the app's own small store.
func (s *Service) Store(ctx context.Context, appID, token string) (json.RawMessage, error) {
	app, err := s.Authorize(ctx, appID, token)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(s.storePath(app.ID))
	if os.IsNotExist(err) {
		return json.RawMessage(`{}`), nil
	}
	if err != nil {
		return nil, err
	}
	if len(raw) > MaxStoreBytes {
		return nil, ErrInvalid
	}
	return json.RawMessage(raw), nil
}

// PutStore writes the app's store without asking the person. It refuses a value
// that is not a JSON object or that exceeds the store limit.
func (s *Service) PutStore(ctx context.Context, appID, token string, value json.RawMessage) error {
	app, err := s.Authorize(ctx, appID, token)
	if err != nil {
		return err
	}
	if len(value) == 0 || len(value) > MaxStoreBytes {
		return ErrInvalid
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(value, &object); err != nil {
		return ErrInvalid
	}
	return writeAtomic(s.storePath(app.ID), value)
}

func writeAtomic(name string, data []byte) error {
	dir := filepath.Dir(name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// A unique temporary file keeps two overlapping writers from sharing bytes.
	f, err := os.CreateTemp(dir, filepath.Base(name)+".tmp-")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err = f.Write(data); err == nil {
		err = f.Close()
	} else {
		_ = f.Close()
	}
	if err == nil {
		err = os.Rename(tmp, name)
	}
	if err != nil {
		_ = os.Remove(tmp)
	}
	return err
}

func (s *Service) snapshotStore(app App) error {
	raw, err := os.ReadFile(s.storePath(app.ID))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	dir := filepath.Join(s.cfg.Root, "miniapps", app.ID, "store-snapshots")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return writeAtomic(filepath.Join(dir, itoa(app.Revision)+".json"), raw)
}

// recipePath keeps the request, checklist and manifest with the app's files so
// a restore can bring back the exact declaration the revision was checked on.
func (s *Service) recipePath(appID string, revision int64) string {
	return filepath.Join(s.cfg.Root, "miniapps", appID, "recipes", itoa(revision)+".json")
}

func (s *Service) writeRecipe(appID string, revision int64, manifest Manifest, checklist []result.MockItem, shape []string) error {
	raw, err := json.Marshal(struct {
		Manifest  Manifest          `json:"manifest"`
		Checklist []result.MockItem `json:"checklist"`
		Shape     []string          `json:"shape,omitempty"`
	}{manifest, checklist, shape})
	if err != nil {
		return err
	}
	return writeAtomic(s.recipePath(appID, revision), raw)
}

func (s *Service) readRecipe(appID string, revision int64) ([]byte, error) {
	return os.ReadFile(s.recipePath(appID, revision))
}
