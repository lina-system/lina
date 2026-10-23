// SPDX-License-Identifier: Apache-2.0
// The files the installer, the guard and the rollback command share. Every
// path they use is listed here so the portable tests and the macOS build agree
// on one layout.
#ifndef LINA_STATE_H
#define LINA_STATE_H

#include <stdbool.h>
#include <stdint.h>

#include "host.h"
#include "rule.h"

#define LINA_PATH_MAX 256
#define LINA_VERIFY_WINDOW_MS 120000u
#define LINA_BACKUP_TEXT_MAX 4096

struct lina_paths {
  char state_dir[LINA_PATH_MAX];
  char backup[LINA_PATH_MAX];
  char pending[LINA_PATH_MAX];
  char enabled[LINA_PATH_MAX];
  char measured[LINA_PATH_MAX];
  char eval_marker[LINA_PATH_MAX];
  char bundle[LINA_PATH_MAX];
  char guard[LINA_PATH_MAX];
  char guard_plist[LINA_PATH_MAX];
};

// The installed layout on macOS.
void lina_paths_default(struct lina_paths *paths);
// The same layout under a prefix, for the rollback check that runs in a
// disposable directory. An empty prefix is the real root.
void lina_paths_prefix(struct lina_paths *paths, const char *prefix);

// The install's manual-unlock check. `started_ms` is when the install wrote it;
// `lock_ms` is 0 until the guard has seen the session lock, then the time of
// that lock. A pending record older than the window without any lock belongs to
// an install that never completed, so the guard restores rather than leaving
// the rule armed.
struct lina_pending {
  uint64_t started_ms;
  uint64_t lock_ms;
};

int lina_pending_read(const struct lina_host *host, const struct lina_paths *paths, struct lina_pending *out);
int lina_pending_write(const struct lina_host *host, const struct lina_paths *paths,
                       const struct lina_pending *pending);

int lina_enabled_write(const struct lina_host *host, const struct lina_paths *paths, const char *socket,
                       uint32_t person_uid, uint32_t deadline_ms);
bool lina_enabled_present(const struct lina_host *host, const struct lina_paths *paths);

struct lina_backup {
  char build[64];
  struct lina_right rule;
};

// 0 on success. The backup file holds the rule, the macOS build and the hash of
// both; the host creates it readable by root only.
int lina_backup_write(const struct lina_host *host, const struct lina_paths *paths,
                      const struct lina_right *rule, const char *build);
// 1 usable, 0 absent, -1 unusable. An unusable backup is never restored.
int lina_backup_read(const struct lina_host *host, const struct lina_paths *paths, struct lina_backup *out);

#endif
