// SPDX-License-Identifier: Apache-2.0
// Every operating-system call the installer, the guard and the rollback command
// need, behind one interface. The portable core never calls Security, launchd,
// CoreGraphics or the file system directly, so the same code runs in the macOS
// tools and in the Linux foundation check against a fake authorization
// database.
//
// The interface deliberately has no network, no shell and no way to write a
// right other than the one the core names. The rollback command therefore runs
// with no Node, no Core and no network.
#ifndef LINA_HOST_H
#define LINA_HOST_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "rule.h"

struct lina_host {
  void *context;

  // 0 found, 1 absent, -1 error. A definition with a value type this interface
  // cannot model is an error, so it is never written back.
  int (*read_right)(void *context, const char *name, struct lina_right *out);
  int (*write_right)(void *context, const char *name, const struct lina_right *right);
  int (*remove_right)(void *context, const char *name);
  // 1 allow, 0 deny, -1 no answer or error. Must return inside the deadline.
  int (*evaluate_right)(void *context, const char *name, uint32_t deadline_ms);

  // Recursive copy of a signed bundle. 0 on success.
  int (*copy_bundle)(void *context, const char *source, const char *destination);
  // 1 signed and notarized, 0 invalid, -1 could not check.
  int (*verify_code)(void *context, const char *path);
  int (*remove_bundle)(void *context, const char *path);
  // Renames within a directory; used to move a verified bundle into place
  // when none is installed.
  int (*rename_path)(void *context, const char *from, const char *to);
  // Exchanges two existing paths in one step, so the installed bundle is never
  // missing while a verified one replaces it. 0 on success; on failure neither
  // path changed.
  int (*swap_paths)(void *context, const char *first, const char *second);
  // Installs or removes the root launchd job that carries the guard.
  int (*install_guard)(void *context, const char *binary, const char *plist);
  int (*remove_guard)(void *context, const char *binary, const char *plist);

  // 1 found (and NUL-terminated), 0 absent, -1 error. Files are read whole.
  int (*read_file)(void *context, const char *path, char *out, size_t capacity, size_t *length);
  // Creates or replaces a file readable by root only. 0 on success.
  int (*write_file)(void *context, const char *path, const void *data, size_t length);
  int (*remove_file)(void *context, const char *path);
  // 1 present, 0 absent, -1 error.
  int (*exists)(void *context, const char *path);
  // 1 present with `age_ms` since it was last written, 0 absent, -1 error. A
  // file dated after the current time reports UINT64_MAX.
  int (*file_age_ms)(void *context, const char *path, uint64_t *age_ms);
  // 0700, root owned. 0 on success.
  int (*make_dir)(void *context, const char *path);

  // 1 when the build string was read. `out` holds the macOS build, e.g. 25A354.
  int (*measure_build)(void *context, char *out, size_t capacity);
  // 1 on Apple silicon, 0 otherwise.
  int (*is_apple_silicon)(void *context);
  // 1 when the eval passed on this build. Reads the installed measured list.
  int (*build_is_measured)(void *context, const char *build);
  // 1 when the eval's test Mac waiver is present. Nothing else waives the check.
  int (*eval_test_mac)(void *context);

  // 1 locked, 0 unlocked. -1 when the state cannot be read.
  int (*session_locked)(void *context, bool *locked);
  uint64_t (*monotonic_ms)(void *context);

  // One line for the system log. Must never carry a credential.
  void (*log_line)(void *context, const char *line);
};

#endif
