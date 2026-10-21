// SPDX-License-Identifier: Apache-2.0
package main

import (
	"fmt"
	"net"
	"os"
	"time"
)

func main() {
	if len(os.Args) != 3 {
		os.Exit(64)
	}
	connection, err := net.DialTimeout("tcp4", os.Args[1], time.Second)
	if err != nil {
		os.Exit(1)
	}
	connection.Close()
	if err := os.WriteFile(os.Args[2], []byte("connected"), 0600); err != nil {
		os.Exit(2)
	}
	fmt.Print("connected")
}
