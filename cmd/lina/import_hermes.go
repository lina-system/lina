// SPDX-License-Identifier: Apache-2.0
package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/thisisjun786/lina/internal/addons/importer/hermes"
	"github.com/thisisjun786/lina/internal/buildinfo"
	"github.com/thisisjun786/lina/internal/core/backup"
	"github.com/thisisjun786/lina/internal/core/boot"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
)

func runImportHermes(args []string) int {
	flags := flag.NewFlagSet("lina import-hermes", flag.ContinueOnError)
	root := flags.String("state-root", "", "installed state root; stop Core before importing")
	source := flags.String("source", "", "Hermes install directory (read only)")
	profile := flags.String("profile", "", "stable source profile identity")
	agent := flags.String("agent", "", "existing destination agent id")
	person := flags.String("person", "", "confirmed person id")
	lcm := flags.String("lcm", "lcm.db", "LCM database path relative to the source directory")
	state := flags.String("state", "state.db", "host database path relative to the source directory")
	owner := flags.String("owner-session", "", "comma-separated confirmed owner CLI sessions; other routes remain unproven")
	rehearse := flags.Bool("rehearse", false, "print rehearsal only, without writing canon")
	confirm := flags.Bool("confirm-import", false, "confirm source profile, destination and owner maps, then import")
	promote := flags.String("promote", "", "reviewed cluster ids, all, or none; accepting attests that the quotes concern the person")
	horizon := flags.Int64("horizon", 0, "exclude dated nonrecurring candidates before this Unix timestamp")
	flags.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: lina import-hermes --state-root <path> --source <path> --profile <id> --agent <id> --person <id> [--owner-session <ids>] [--rehearse]\nPrints rehearsal first, then asks to import and separately to promote reviewed clusters. Stop Core before importing.")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 64
	}
	if *root == "" || *source == "" || *profile == "" || *agent == "" || *person == "" || flags.NArg() != 0 || *horizon < 0 {
		flags.Usage()
		return 64
	}
	if *rehearse && (*confirm || *promote != "") {
		flags.Usage()
		return 64
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	err := importHermes(ctx, *root, *source, *lcm, *state, hermes.Profile{ID: *profile, AgentID: *agent, PersonID: *person}, *owner, *rehearse, *confirm, *promote, hermes.PromotionPolicy{Horizon: *horizon}, os.Stdin, os.Stdout)
	if errors.Is(err, ctlfiles.ErrLocked) {
		fmt.Fprintln(os.Stderr, "lina import-hermes: Core is running. Stop Core first, then rerun the command.")
		return 69
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "lina import-hermes: %v\n", err)
		return 78
	}
	return 0
}

func importHermes(ctx context.Context, root, source, lcm, state string, profile hermes.Profile, owner string, rehearse, confirm bool, promote string, policy hermes.PromotionPolicy, input io.Reader, output io.Writer) error {
	source, err := filepath.EvalSymlinks(source)
	if err != nil {
		return err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	source, err = filepath.Abs(source)
	if err != nil {
		return err
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return err
	}
	// Neither temporary files nor destination writes may fall inside the source.
	for _, pair := range [][2]string{{source, root}, {root, source}} {
		rel, err := filepath.Rel(pair[0], pair[1])
		if err != nil {
			return err
		}
		if rel == "." || rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return errors.New("source and destination must be separate directories")
		}
	}
	staging, err := os.MkdirTemp("", "lina-import-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	for _, entry := range []struct {
		name string
		kind hermes.SQLiteSource
		dest *string
	}{{lcm, hermes.HermesLCM, &profile.LCMPath}, {state, hermes.HermesState, &profile.StatePath}} {
		if entry.name == "" {
			continue
		}
		if !filepath.IsLocal(entry.name) {
			return errors.New("database paths must stay within the source directory")
		}
		target := filepath.Join(staging, string(entry.kind)+".sqlite")
		if err = hermes.SnapshotSQLite(ctx, filepath.Join(source, entry.name), target, entry.kind); err != nil {
			return err
		}
		*entry.dest = target
	}
	if profile.LCMPath == "" && profile.StatePath == "" {
		return errors.New("no source database selected")
	}
	for _, session := range strings.Split(owner, ",") {
		if session = strings.TrimSpace(session); session != "" {
			profile.OwnerChannels = append(profile.OwnerChannels, hermes.OwnerChannel{Source: "cli", SessionID: session})
		}
	}
	plan, err := hermes.Prepare(ctx, hermes.Request{BatchID: "import-" + rand.Text(), Confirmed: true, Profiles: []hermes.Profile{profile}})
	if err != nil {
		return err
	}
	defer plan.Close()
	report, err := plan.Rehearse()
	if err != nil {
		return err
	}
	encode := json.NewEncoder(output)
	encode.SetIndent("", "  ")
	if err = encode.Encode(struct{ Rehearsal hermes.RehearsalReport }{report}); err != nil {
		return err
	}
	if rehearse {
		return nil
	}
	scanner := bufio.NewScanner(input)
	answer := func(prompt string) (string, error) {
		if _, err := fmt.Fprintln(output, prompt); err != nil {
			return "", err
		}
		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				return "", err
			}
			return "", io.EOF
		}
		return strings.TrimSpace(scanner.Text()), nil
	}
	if !confirm {
		a, err := answer("Confirm the profile, agent, person and owner-session maps above. Type import to copy and seal this source, or anything else to stop:")
		if err != nil {
			return err
		}
		if a != "import" {
			return nil
		}
	}
	controls, err := ctlfiles.Open(root)
	if err != nil {
		return err
	}
	defer controls.Close()
	if err = controls.Acquire(); err != nil {
		return err
	}
	if err = backup.CheckRestore(root); err != nil {
		return err
	}
	for _, name := range []string{"canon/canon.sqlite", "system/bin/manifest.json"} {
		if err = backupRegularPath(root, name); err != nil {
			return err
		}
	}
	identity, err := controls.Read("main.json")
	if err != nil {
		return err
	}
	var main struct {
		MainID string `json:"main_id"`
	}
	if err = json.Unmarshal(identity, &main); err != nil {
		return err
	}
	if main.MainID == "" {
		return errors.New("missing installed identity")
	}
	epoch, err := controls.Epoch()
	if err != nil {
		return err
	}
	stored, err := canon.PeekEpoch(ctx, filepath.Join(root, "canon/canon.sqlite"))
	if err != nil {
		return err
	}
	epoch = max(epoch, stored)
	if epoch == 0 {
		return errors.New("start Core once before importing")
	}
	db, err := canon.Open(ctx, canon.Config{Path: filepath.Join(root, "canon/canon.sqlite"), Epoch: epoch, Controls: controls, Migrations: boot.ApplicationMigrations()})
	if err != nil {
		return err
	}
	defer db.Close(context.Background())
	// Explicit ids must name an installed agent and its actual owner. Never
	// silently create an unreachable history under a misspelled destination.
	err = db.Read(ctx, func(tx *canon.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM agents a JOIN agent_revisions r ON r.agent_id=a.id AND r.revision=a.revision WHERE a.id=? AND a.trashed_at=0 AND r.person_id=?`, profile.AgentID, profile.PersonID).Scan(&n); err != nil {
			return err
		}
		if n != 1 {
			return errors.New("destination agent and person do not match an active installed agent")
		}
		return nil
	})
	if err != nil {
		return err
	}
	imported, err := plan.Import(ctx, db)
	if err != nil {
		return err
	}
	capture := func(ctx context.Context) (string, error) {
		m, err := backup.Create(ctx, backup.Config{Root: root, Canon: db, Controls: controls, MainID: main.MainID, Epoch: epoch, Artifacts: map[string]string{"lina": buildinfo.String(), "sqlite_wasm": canon.SQLiteWASMSHA256}})
		return m.GenerationID, err
	}
	seal, err := plan.Seal(ctx, db, capture)
	if err != nil {
		return err
	}
	batches, err := plan.PromotionBatches(policy)
	if err != nil {
		return err
	}
	if err = encode.Encode(struct {
		Import    hermes.Report
		Seal      hermes.SealReport
		Promotion []hermes.PromotionBatch
	}{imported, seal, batches}); err != nil {
		return err
	}
	if promote == "" {
		promote, err = answer("Review the quoted clusters above. Accept only statements about you: enter cluster ids separated by commas, all, or none. Persona proposals cannot become memory:")
		if err != nil {
			return err
		}
	}
	var accepted []string
	switch promote {
	case "all":
		for _, b := range batches {
			if !b.PersonaProposal {
				accepted = append(accepted, b.ID)
			}
		}
	case "none":
	default:
		for _, id := range strings.Split(promote, ",") {
			accepted = append(accepted, strings.TrimSpace(id))
		}
	}
	seal, n, err := plan.Finish(ctx, db, policy, accepted, "confirmation-"+rand.Text(), capture)
	if err != nil {
		return err
	}
	return encode.Encode(struct {
		Promoted         int
		BackupGeneration string
	}{n, seal.BackupGeneration})
}
