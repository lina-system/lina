// SPDX-License-Identifier: Apache-2.0

module github.com/thisisjun786/lina

go 1.27

toolchain go1.27.3

replace github.com/thisisjun786/lina/kit => ./kit

require (
	github.com/nishanths/exhaustive v0.15.1 // indirect
	golang.org/x/mod v0.41.0 // indirect
	golang.org/x/sync v0.23.0 // indirect
	golang.org/x/tools v0.51.0 // indirect
)

tool github.com/nishanths/exhaustive/cmd/exhaustive
