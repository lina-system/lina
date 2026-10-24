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

	"github.com/thisisjun786/lina/internal/addons/importer/murmur"
	"github.com/thisisjun786/lina/internal/buildinfo"
	"github.com/thisisjun786/lina/internal/core/assets"
	"github.com/thisisjun786/lina/internal/core/backup"
	"github.com/thisisjun786/lina/internal/core/boot"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/messenger"
	"github.com/thisisjun786/lina/internal/core/retract"
)

func runImportMurmur(args []string) int {
	flags := flag.NewFlagSet("lina import-murmur", flag.ContinueOnError)
	root := flags.String("state-root", "", "installed state root; stop Core before importing")
	source := flags.String("source", "", "murmur data home (read only)")
	person := flags.String("person", "", "confirmed person id")
	instance := flags.String("instance", "default", "stable identity of this data home")
	var own []string
	flags.Func("own", "confirmed own author id per source, as <source>:<author id> (repeatable)", func(value string) error {
		own = append(own, value)
		return nil
	})
	learning := flags.Bool("learning", false, "learn from the imported messages (the card states the count and an estimated cost)")
	rehearse := flags.Bool("rehearse", false, "print rehearsal only, without writing canon")
	confirm := flags.Bool("confirm-import", false, "confirm the source, person and own-author maps, then import")
	flags.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: lina import-murmur --state-root <path> --source <path> --person <id> [--instance <id>] [--own <source>:<author id>] [--learning] [--rehearse]\nPrints a rehearsal first, then asks to import. Stop Core before importing.")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 64
	}
	if *root == "" || *source == "" || *person == "" || flags.NArg() != 0 {
		flags.Usage()
		return 64
	}
	if *rehearse && *confirm {
		flags.Usage()
		return 64
	}
	profile, err := murmurProfile(*person, *instance, own)
	if err != nil {
		fmt.Fprintf(os.Stderr, "lina import-murmur: %v\n", err)
		return 64
	}
	profile.Learning = *learning
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	err = importMurmur(ctx, *root, *source, profile, *rehearse, *confirm, os.Stdin, os.Stdout)
	if errors.Is(err, ctlfiles.ErrLocked) {
		fmt.Fprintln(os.Stderr, "lina import-murmur: Core is running. Stop Core first, then rerun the command.")
		return 69
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "lina import-murmur: %v\n", err)
		return 78
	}
	return 0
}

// murmurProfile reads the confirmed own-author map. A source named twice, an
// unknown source or an empty map for a source present in the store is refused
// before any canon write.
func murmurProfile(person, instance string, own []string) (murmur.Profile, error) {
	profile := murmur.Profile{Person: person, Instance: instance, Own: map[string]string{}}
	for _, entry := range own {
		source, id, ok := strings.Cut(entry, ":")
		if !ok || source == "" || id == "" {
			return murmur.Profile{}, errors.New("--own requires <source>:<author id>")
		}
		if _, known := murmur.Platforms[source]; !known {
			return murmur.Profile{}, errors.New("--own requires a murmur source: kakao, discord or imessage")
		}
		if _, taken := profile.Own[source]; taken {
			return murmur.Profile{}, errors.New("--own names one source twice")
		}
		profile.Own[source] = id
	}
	return profile, nil
}

func importMurmur(ctx context.Context, root, home string, profile murmur.Profile, rehearse, confirm bool, input io.Reader, output io.Writer) error {
	home, err := filepath.Abs(home)
	if err != nil {
		return err
	}
	if home, err = filepath.EvalSymlinks(home); err != nil {
		return err
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return err
	}
	if root, err = filepath.EvalSymlinks(root); err != nil {
		return err
	}
	staging, err := os.MkdirTemp("", "lina-murmur-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	plan, err := murmur.Prepare(ctx, murmur.Request{Home: home, Staging: staging, Profile: profile})
	if err != nil {
		return err
	}
	defer plan.Close()
	encode := json.NewEncoder(output)
	encode.SetIndent("", "  ")
	report := plan.Rehearse()
	if err = encode.Encode(struct{ Rehearsal murmur.Report }{report}); err != nil {
		return err
	}
	if rehearse {
		return nil
	}
	answer := func(prompt string) (string, error) {
		if _, err := fmt.Fprintln(output, prompt); err != nil {
			return "", err
		}
		scanner := bufio.NewScanner(input)
		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				return "", err
			}
			return "", io.EOF
		}
		return strings.TrimSpace(scanner.Text()), nil
	}
	if !confirm {
		text, err := answer("Confirm the maps above. Type import to copy these messages into LINA, or anything else to stop:")
		if err != nil {
			return err
		}
		if text != "import" {
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
	materials, err := assets.Open(ctx, root, db)
	if err != nil {
		return err
	}
	defer materials.Close()
	writer, err := assets.OpenMaterialWriter(ctx, materials)
	if err != nil {
		return err
	}
	removals, err := retract.Open(ctx, retract.Config{Canon: db, Root: root})
	if err != nil {
		return err
	}
	defer removals.Close()
	store := messenger.Open(db, boot.LedgerMaterials(materials, writer, assets.Actor{PersonID: profile.Person}), removals)
	capture := func(ctx context.Context) (string, error) {
		manifest, err := backup.Create(ctx, backup.Config{Root: root, Canon: db, Controls: controls, MainID: main.MainID, Epoch: epoch, Artifacts: map[string]string{"lina": buildinfo.String(), "sqlite_wasm": canon.SQLiteWASMSHA256}})
		return manifest.GenerationID, err
	}
	batch := "murmur-" + rand.Text()
	imported, err := plan.Import(ctx, store, batch, capture)
	if err != nil {
		return err
	}
	return encode.Encode(struct {
		Import murmur.ImportReport
	}{imported})
}
