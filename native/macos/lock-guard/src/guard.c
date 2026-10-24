// SPDX-License-Identifier: Apache-2.0
#include "guard.h"

#include <stdio.h>
#include <string.h>

static void log_line(const struct lina_host *host, const char *message) {
  host->log_line(host->context, message);
}

int lina_restore_backup(const struct lina_host *host, const struct lina_paths *paths, const char *reason,
                        bool *used_stock) {
  struct lina_backup backup;
  struct lina_right stock;
  const struct lina_right *target = NULL;
  bool stock_used = false;
  int state = lina_backup_read(host, paths, &backup);
  if (state == 1) {
    target = &backup.rule;
  } else {
    lina_rule_stock(&stock);
    target = &stock;
    stock_used = true;
  }
  if (used_stock != NULL) *used_stock = stock_used;
  int written = host->write_right(host->context, LINA_RIGHT_SCREENSAVER, target);
  struct lina_right read_back;
  bool matches = written == 0 && host->read_right(host->context, LINA_RIGHT_SCREENSAVER, &read_back) == 0
      && lina_right_equal_ignoring_timestamps(&read_back, target);
  // The feature is off from here on, whatever the write did: the plug-in denies
  // at once and the guard writes no rule again until another install.
  host->remove_file(host->context, paths->enabled);
  host->remove_file(host->context, paths->pending);
  if (!matches) {
    // Turning the feature off removed what triggered this restore, so the
    // retry marker is what brings the guard back to it.
    const char *text = reason == NULL ? "" : reason;
    host->write_file(host->context, paths->restore_retry, text, strlen(text));
    log_line(host, "restore failed: rule write");
    return -1;
  }
  host->remove_file(host->context, paths->restore_retry);
  host->remove_file(host->context, paths->backup);
  char line[160];
  snprintf(line, sizeof(line), "restored %s%s", reason == NULL ? "" : reason, stock_used ? " from stock" : "");
  log_line(host, line);
  return 0;
}

struct lina_guard_report lina_guard_restore(const struct lina_host *host, const struct lina_paths *paths,
                                            const char *reason) {
  struct lina_guard_report report = {LINA_GUARD_IDLE, reason, false};
  if (lina_restore_backup(host, paths, reason, &report.used_stock) != 0) {
    report.action = LINA_GUARD_FAILED;
    return report;
  }
  report.action = LINA_GUARD_RESTORED;
  return report;
}

static struct lina_guard_report restore(const struct lina_host *host, const struct lina_paths *paths,
                                        const char *reason) {
  return lina_guard_restore(host, paths, reason);
}

struct lina_guard_report lina_guard_tick(const struct lina_host *host, const struct lina_paths *paths,
                                         uint64_t now_ms) {
  struct lina_guard_report report = {LINA_GUARD_IDLE, NULL, false};
  struct lina_pending pending;
  int pending_state = lina_pending_read(host, paths, &pending);
  struct lina_right rule;
  int rule_state = host->read_right(host->context, LINA_RIGHT_SCREENSAVER, &rule);
  bool names_lina = rule_state == 0 && lina_rule_names_lina(&rule);
  bool enabled = lina_enabled_present(host, paths);
  struct lina_backup backup;
  int backup_state = lina_backup_read(host, paths, &backup);
  bool retry = host->exists(host->context, paths->restore_retry) == 1;
  // Nothing of LINA's is installed, so the guard has nothing to restore and
  // writes nothing.
  if (!enabled && !names_lina && pending_state != 1 && backup_state != 1 && !retry) return report;

  if (retry) return restore(host, paths, "restore-retry");

  if (pending_state == 1) {
    // A clock that reads earlier than the record was written means the Mac
    // restarted; the earlier lock, if any, is no longer the one to measure.
    if (now_ms < pending.started_ms || (pending.lock_ms != 0 && now_ms < pending.lock_ms)) {
      pending.started_ms = now_ms;
      pending.lock_ms = 0;
      lina_pending_write(host, paths, &pending);
    }
    // The install asked for one lock and one manual unlock. An unlock first
    // seen after the window cannot prove it came inside it, so the rule
    // returns to the backup before the session is read.
    if (pending.lock_ms != 0 && now_ms >= pending.lock_ms + LINA_VERIFY_WINDOW_MS) {
      return restore(host, paths, "verify-timeout");
    }
    bool locked = false;
    if (host->session_locked(host->context, &locked) == 0) {
      if (locked) {
        if (pending.lock_ms == 0) {
          pending.lock_ms = now_ms;
          lina_pending_write(host, paths, &pending);
        }
      } else if (pending.lock_ms != 0) {
        host->remove_file(host->context, paths->pending);
        log_line(host, "install verified by a manual unlock");
        report.action = LINA_GUARD_VERIFIED;
        return report;
      }
    }
  }

  if (names_lina) {
    if (host->exists(host->context, paths->bundle) != 1) return restore(host, paths, "bundle-missing");
    int verified = host->verify_code(host->context, paths->bundle);
    if (verified == 0) return restore(host, paths, "signature-invalid");
    if (verified < 0) return restore(host, paths, "signature-unreadable");
    struct lina_right unlock;
    if (host->read_right(host->context, LINA_RIGHT_UNLOCK, &unlock) != 0) {
      return restore(host, paths, "right-missing");
    }
  }

  if (enabled) {
    if (rule_state != 0) return restore(host, paths, "rule-unreadable");
    if (!lina_rule_is_lina(&rule)) return restore(host, paths, "rule-changed");
    char build[64];
    if (host->measure_build(host->context, build, sizeof(build)) != 1) {
      return restore(host, paths, "build-unknown");
    }
    if (!host->eval_test_mac(host->context) && !host->build_is_measured(host->context, build)) {
      return restore(host, paths, "build-unmeasured");
    }
    // The install passed every check, so a marker left by a crashed
    // evaluation no longer needs to hold later evaluations back.
    uint64_t age = 0;
    if (host->file_age_ms(host->context, paths->evaluation, &age) == 1 && age >= LINA_EVALUATION_STALE_MS
        && host->remove_file(host->context, paths->evaluation) == 0) {
      log_line(host, "cleared the marker of a crashed evaluation");
    }
  }
  return report;
}
