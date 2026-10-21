// SPDX-License-Identifier: Apache-2.0

// policygen emits classic BPF for seccomp_data. No compiler or libseccomp is
// needed on the installed host. Run from kit/sandbox with go generate.
package main

import (
	"encoding/binary"
	"os"
	"path/filepath"
)

type instruction struct {
	Code   uint16
	JT, JF uint8
	K      uint32
}

func main() {
	for _, arch := range []struct {
		name          string
		audit         uint32
		socket, clone uint32
		denied        []uint32
		network       []uint32
	}{
		{"amd64", 0xc000003e, 41, 56, []uint32{101, 310, 311, 248, 249, 250, 321, 298, 323, 165, 166, 155, 304, 308, 272, 112, 109}, []uint32{42, 43, 49, 50, 44, 46, 299, 307, 288}},
		{"arm64", 0xc00000b7, 198, 220, []uint32{117, 270, 271, 217, 218, 219, 280, 241, 282, 40, 39, 41, 265, 268, 97, 157, 154}, []uint32{203, 202, 200, 201, 206, 211, 243, 269, 242}},
	} {
		for _, network := range []bool{false, true} {
			p := []instruction{{0x20, 0, 0, 4}, {0x15, 1, 0, arch.audit}, {0x06, 0, 0, 0x80000000}, {0x20, 0, 0, 0}}
			// x32 shares the x86 audit architecture but has different syscall numbers.
			p = append(p, instruction{0x45, 0, 1, 0x40000000}, instruction{0x06, 0, 0, 0x80000000})
			deny := append([]uint32{}, arch.denied...)
			deny = append(deny, 425, 426, 427, 428, 429, 430, 431, 432, 433, 442)
			if !network {
				deny = append(deny, arch.network...)
			}
			for _, nr := range deny {
				p = append(p, instruction{0x15, 0, 1, nr}, instruction{0x06, 0, 0, 0x50001})
			}
			// clone3 has an indirect flags struct; force libc/Go to use filtered clone.
			p = append(p, instruction{0x15, 0, 1, 435}, instruction{0x06, 0, 0, 0x50026})
			p = append(p, instruction{0x15, 0, 4, arch.clone}, instruction{0x20, 0, 0, 16}, instruction{0x45, 0, 1, 0x7e020000}, instruction{0x06, 0, 0, 0x50001}, instruction{0x06, 0, 0, 0x7fff0000})
			// Deny socket families that bypass IP/Unix isolation (especially AF_VSOCK).
			p = append(p, instruction{0x15, 0, 0, arch.socket})
			socketJump := len(p) - 1
			p = append(p, instruction{0x20, 0, 0, 16}, instruction{0x15, 0, 1, 1}, instruction{0x06, 0, 0, 0x7fff0000})
			if network {
				for _, family := range []uint32{2, 10} {
					p = append(p, instruction{0x15, 0, 1, family}, instruction{0x06, 0, 0, 0x7fff0000})
				}
			}
			p = append(p, instruction{0x06, 0, 0, 0x50001})
			p[socketJump].JF = uint8(len(p) - socketJump - 1)
			p = append(p, instruction{0x06, 0, 0, 0x7fff0000})
			mode := "blocked"
			if network {
				mode = "network"
			}
			file, err := os.Create(filepath.Join("policy", "linux_"+arch.name+"_"+mode+".bpf"))
			if err != nil {
				panic(err)
			}
			if err := binary.Write(file, binary.LittleEndian, p); err != nil {
				panic(err)
			}
			if err := file.Close(); err != nil {
				panic(err)
			}
		}
	}
}
