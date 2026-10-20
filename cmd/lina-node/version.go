// SPDX-License-Identifier: Apache-2.0
package main

import (
	"fmt"

	"github.com/thisisjun786/lina/internal/buildinfo"
)

func version() {
	fmt.Println(buildinfo.String())
}
