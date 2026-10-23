// SPDX-License-Identifier: Apache-2.0
package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	a2a "github.com/thisisjun786/lina/internal/core/a2a/publication"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/secrets"
)

func runA2APublication(args []string) int {
	flags := flag.NewFlagSet("lina a2a publish", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	root := flags.String("state-root", "", "installed state root")
	file := flags.String("config", "", "owner publication configuration")
	credential := flags.Bool("credential-stdin", false, "read the introspection client secret from stdin")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 64
	}
	if *root == "" || *file == "" || flags.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: lina a2a publish --state-root <path> --config <file> [--credential-stdin]")
		return 64
	}
	input, err := os.Open(*file)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Publication configuration is unavailable.")
		return 78
	}
	raw, err := io.ReadAll(io.LimitReader(input, (64<<10)+1))
	input.Close()
	if err != nil || len(raw) > 64<<10 {
		fmt.Fprintln(os.Stderr, "Publication configuration is too large.")
		return 78
	}
	var source io.Reader = strings.NewReader("")
	if *credential {
		source = os.Stdin
	}
	if err = configureA2A(*root, raw, source); err != nil {
		if errors.Is(err, ctlfiles.ErrLocked) {
			fmt.Fprintln(os.Stderr, "Stop Core before changing publication setup.")
		} else {
			fmt.Fprintln(os.Stderr, "Publication configuration could not be saved.")
		}
		return 78
	}
	fmt.Fprintln(os.Stdout, "Publication configuration saved. Start Core to apply it.")
	return 0
}
func configureA2A(root string, raw []byte, credential io.Reader) error {
	cfg, err := a2a.ParseConfig(raw)
	if err != nil {
		return err
	}
	controls, err := ctlfiles.Open(root)
	if err != nil {
		return err
	}
	defer controls.Close()
	if err = controls.Acquire(); err != nil {
		return err
	}
	if cfg.Enabled {
		data, err := controls.Read("main.json")
		if err != nil {
			return err
		}
		var identity struct {
			MainID string `json:"main_id"`
		}
		if json.Unmarshal(data, &identity) != nil || identity.MainID == "" {
			return a2a.ErrInvalid
		}
		password, err := io.ReadAll(io.LimitReader(credential, 8193))
		if err != nil || len(password) > 8192 {
			return a2a.ErrInvalid
		}
		defer clear(password)
		vault, err := secrets.OpenFiles(root)
		if err != nil {
			return err
		}
		defer vault.Close()
		key := secrets.Key{Person: "person:" + identity.MainID, Service: a2a.CredentialService(cfg.OAuth), Name: cfg.OAuth.SecretName}
		if len(password) > 0 {
			value := strings.TrimSuffix(strings.TrimSuffix(string(password), "\n"), "\r")
			if value == "" || strings.ContainsAny(value, "\r\n\x00") {
				return a2a.ErrInvalid
			}
			if err = vault.Put(key, []byte(base64.StdEncoding.EncodeToString([]byte(cfg.OAuth.ClientID+":"+value)))); err != nil {
				return err
			}
		} else if _, err = vault.Get(key); err != nil {
			return err
		}
	}
	return controls.Write("a2a.json", raw)
}
