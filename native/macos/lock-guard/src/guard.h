// SPDX-License-Identifier: Apache-2.0
// The lock guard: one restore path, and no other rule write. The guard is the
// only LINA part that can put the lock screen's rule back, on request or when
// it finds one of the failures the design lists.
#ifndef LINA_GUARD_H
#define LINA_GUARD_H

#include <stdbool.h>
#include <stdint.h>

#include "host.h"
#include "state.h"

enum lina_guard_action {
  LINA_GUARD_IDLE = 0,
  // The person locked and unlocked by hand; the install is verified.
  LINA_GUARD_VERIFIED,
  LINA_GUARD_RESTORED,
  LINA_GUARD_FAILED,
};

struct lina_guard_report {
  enum lina_guard_action action;
  const char *reason;
  bool used_stock;
};

// One pass over the state: the pending manual unlock, the bundle and its
// signature while the rule names it, LINA's unlock right, the rule itself and
// the measured build. `now_ms` is the injected clock.
struct lina_guard_report lina_guard_tick(const struct lina_host *host, const struct lina_paths *paths,
                                         uint64_t now_ms);

// The restore on request. Writes the backup, or the stock rule when the backup
// is unusable, and turns the feature off. It writes no other rule.
struct lina_guard_report lina_guard_restore(const struct lina_host *host, const struct lina_paths *paths,
                                            const char *reason);

// Shared with the installer's abort path: restore, turn the feature off, and
// report whether the stock rule stood in for an unusable backup. 0 on success.
int lina_restore_backup(const struct lina_host *host, const struct lina_paths *paths, const char *reason,
                        bool *used_stock);

#endif
