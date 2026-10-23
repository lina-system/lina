// SPDX-License-Identifier: Apache-2.0
package messages

import (
	_ "embed"
	"io"
	"strings"

	messageconfig "github.com/thisisjun786/lina/kit/provider/messages/config"
)

// Config is shared with build-metadata readers independently of the transport.
type Config = messageconfig.Config

func ReadConfig(r io.Reader) (Config, error) { return messageconfig.ReadConfig(r) }

//go:embed manifest.json
var manifest string

// BuildConfig reads the same request policy that is shipped in the release
// manifest. Runtime endpoint settings never change its fields or retry policy.
func BuildConfig() (Config, error) {
	return ReadConfig(strings.NewReader(`{"providers":` + manifest + `}`))
}
