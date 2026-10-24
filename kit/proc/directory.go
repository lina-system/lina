// SPDX-License-Identifier: Apache-2.0

package proc

import "strings"

// joinOwnerDirectory prefixes a relative directory with the owner's working
// directory. The job helper runs in a private directory, so the command's
// directory has to be chosen before the job starts.
//
// The result is not cleaned. Cleaning "link/.." lexically would drop the
// symlink, and the kernel would then select the owner's directory instead of
// the symlink's parent.
func joinOwnerDirectory(wd, dir string) string {
	if dir == "" {
		return wd
	}
	return strings.TrimSuffix(wd, "/") + "/" + dir
}
