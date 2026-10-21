// SPDX-License-Identifier: Apache-2.0
package responses

import (
	"io"

	responseconfig "github.com/thisisjun786/lina/kit/provider/responses/config"
)

const SDKModule = responseconfig.SDKModule
const SDKVersion = responseconfig.SDKVersion

// Config is shared with build-metadata readers that do not need the SDK.
type Config = responseconfig.Config

func ReadConfig(r io.Reader) (Config, error) { return responseconfig.ReadConfig(r) }
