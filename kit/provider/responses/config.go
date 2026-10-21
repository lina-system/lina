// SPDX-License-Identifier: Apache-2.0
package responses

import (
	"io"

	responseconfig "github.com/thisisjun786/lina/kit/provider/responses/config"
)

// Config is shared with build-metadata readers independently of the HTTP/SSE transport.
type Config = responseconfig.Config

func ReadConfig(r io.Reader) (Config, error) { return responseconfig.ReadConfig(r) }
