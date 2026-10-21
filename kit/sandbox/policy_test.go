// SPDX-License-Identifier: Apache-2.0

package sandbox_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Test both shipped architectures without needing a host of each architecture.
// Evaluate the actual classic-BPF artifacts, not a second policy generator.
func TestShippedSeccompPolicies(t *testing.T) {
	for _, arch := range []struct {
		name                                  string
		audit, socket, clone, ptrace, connect uint32
	}{
		{"amd64", 0xc000003e, 41, 56, 101, 42}, {"arm64", 0xc00000b7, 198, 220, 117, 203},
	} {
		for _, mode := range []string{"blocked", "network"} {
			data, err := os.ReadFile("policy/linux_" + arch.name + "_" + mode + ".bpf")
			if err != nil {
				t.Fatal(err)
			}
			allow, deny, kill := uint32(0x7fff0000), uint32(0x50001), uint32(0x80000000)
			for _, tc := range []struct {
				name                 string
				audit, nr, arg, want uint32
			}{
				{"wrong architecture", 0, 0, 0, kill}, {"x32", arch.audit, 0x40000000, 0, kill},
				{"ordinary syscall", arch.audit, 1, 0, allow}, {"ptrace", arch.audit, arch.ptrace, 0, deny},
				{"io_uring", arch.audit, 425, 0, deny}, {"clone3 fallback", arch.audit, 435, 0, 0x50026},
				{"thread", arch.audit, arch.clone, 0x100, allow}, {"namespace", arch.audit, arch.clone, 0x10000000, deny},
				{"VM socket", arch.audit, arch.socket, 40, deny},
				{"Unix socket aliases", arch.audit, arch.socket, 1, deny},
			} {
				if got := evalBPF(t, data, tc.audit, tc.nr, tc.arg); got != tc.want {
					t.Fatalf("%s %s %s: %#x want %#x", arch.name, mode, tc.name, got, tc.want)
				}
			}
			expected := deny
			if mode == "network" {
				expected = allow
			}
			for _, nr := range []uint32{arch.socket, arch.connect} {
				if got := evalBPF(t, data, arch.audit, nr, 2); got != expected {
					t.Fatalf("%s %s network policy: %#x", arch.name, mode, got)
				}
			}
		}
	}
}
func evalBPF(t *testing.T, program []byte, arch, nr, arg uint32) uint32 {
	t.Helper()
	var a uint32
	for pc := 0; pc < len(program)/8; pc++ {
		ins := program[pc*8 : pc*8+8]
		op := binary.LittleEndian.Uint16(ins)
		k := binary.LittleEndian.Uint32(ins[4:])
		switch op {
		case 0x20:
			switch k {
			case 0:
				a = nr
			case 4:
				a = arch
			case 16:
				a = arg
			default:
				t.Fatalf("unexpected data load %d", k)
			}
		case 0x15:
			if a == k {
				pc += int(ins[2])
			} else {
				pc += int(ins[3])
			}
		case 0x45:
			if a&k != 0 {
				pc += int(ins[2])
			} else {
				pc += int(ins[3])
			}
		case 0x06:
			return k
		default:
			t.Fatalf("unexpected BPF opcode %x", op)
		}
	}
	t.Fatal("BPF fell off program")
	return 0
}
func TestSeccompArtifactsReproducible(t *testing.T) {
	source, err := filepath.Abs("internal/policygen/main.go")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "policy"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "go", "run", source)
	command.Dir = dir
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("policy compiler: %v %s", err, output)
	}
	files, err := filepath.Glob("policy/*.bpf")
	if err != nil || len(files) != 4 {
		t.Fatal("missing shipped architectures")
	}
	for _, name := range files {
		shipped, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		rebuilt, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(shipped, rebuilt) {
			t.Errorf("stale compiled policy: %s", name)
		}
	}
}
