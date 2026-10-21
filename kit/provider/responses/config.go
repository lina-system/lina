// SPDX-License-Identifier: Apache-2.0
package responses

import (
	_ "embed"
	"io"
	"strings"

	responseconfig "github.com/thisisjun786/lina/kit/provider/responses/config"
)

// Config is shared with build-metadata readers independently of the HTTP/SSE transport.
type Config = responseconfig.Config

func ReadConfig(r io.Reader) (Config, error) { return responseconfig.ReadConfig(r) }

//go:embed manifest.json
var manifest string

// BuildConfig reads the same request policy that is shipped in the release
// manifest. Runtime endpoint settings never change its fields or retry policy.
func BuildConfig() (Config, error) {
	return ReadConfig(strings.NewReader(`{"providers":` + manifest + `}`))
}
