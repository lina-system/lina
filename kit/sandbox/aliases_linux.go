// SPDX-License-Identifier: Apache-2.0
//go:build linux

package sandbox

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// A readonly root mount can expose preexisting aliases of a denied path. Refuse
// such host layouts rather than advertise confinement that path masks cannot
// provide. This runs on every normalization, including immediately before Run.
func checkProtectedPaths(paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	file, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return err
	}
	data, err := io.ReadAll(io.LimitReader(file, (4<<20)+1))
	file.Close()
	if err != nil {
		return err
	}
	if len(data) > 4<<20 {
		return errors.New("mount table exceeds policy bound")
	}
	if err := checkMountAliases(paths, string(data)); err != nil {
		return err
	}
	entries := 0
	for _, path := range paths {
		err := filepath.WalkDir(path, func(_ string, entry os.DirEntry, err error) error {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			if err != nil {
				return err
			}
			entries++
			if entries > 100000 {
				return errors.New("protected tree exceeds policy bound")
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if info.Mode().IsRegular() {
				stat, ok := info.Sys().(*syscall.Stat_t)
				if !ok || stat.Nlink != 1 {
					return errors.New("protected file has an alternate hardlink")
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func checkMountAliases(protected []string, data string) error {
	type mount struct{ device, root, point string }
	var mounts []mount
	unescape := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	for _, line := range strings.Split(strings.TrimSpace(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 10 {
			return errors.New("invalid mount table")
		}
		m := mount{fields[2], unescape.Replace(fields[3]), unescape.Replace(fields[4])}
		// Namespace handles are mounted pseudo-files, not aliases of a host
		// filesystem tree. Their root is a handle such as mnt:[...], not a path.
		separator := -1
		for i, field := range fields {
			if field == "-" {
				separator = i
				break
			}
		}
		if separator >= 0 && separator+1 < len(fields) && fields[separator+1] == "nsfs" {
			continue
		}
		if !filepath.IsAbs(m.root) || !filepath.IsAbs(m.point) {
			return errors.New("invalid mount path")
		}
		mounts = append(mounts, m)
	}
	covered := func(path string) bool {
		for _, deny := range protected {
			if beneath(path, deny) {
				return true
			}
		}
		return false
	}
	for _, deny := range protected {
		for _, source := range mounts {
			// Include mounts inside the protected tree as well as its enclosing
			// mount, since each can have an alias on another host path.
			var object string
			switch {
			case beneath(deny, source.point):
				relative, _ := filepath.Rel(source.point, deny)
				object = filepath.Join(source.root, relative)
			case beneath(source.point, deny):
				object = source.root
			default:
				continue
			}
			for _, target := range mounts {
				if target.device != source.device {
					continue
				}
				var alias string
				switch {
				case beneath(object, target.root):
					relative, _ := filepath.Rel(target.root, object)
					alias = filepath.Join(target.point, relative)
				case beneath(target.root, object):
					alias = target.point
				default:
					continue
				}
				if !covered(alias) {
					return errors.New("protected tree has an unmasked mount alias")
				}
			}
		}
	}
	return nil
}
