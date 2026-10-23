// SPDX-License-Identifier: Apache-2.0
#include "install.h"

#include <stdio.h>
#include <string.h>

#include "guard.h"
#include "sha256.h"

struct attempt {
  bool backup;
  bool bundle;
  bool guard;
  bool rights;
  bool rule;
  bool enabled;
  bool pending;
  bool measured;
};

// The staged bundle lives beside the installed one; swapping it in is a rename,
// so the name the rule uses never points at a partly copied bundle.
static bool staged_bundle(const struct lina_paths *paths, char *out, size_t capacity) {
  size_t length = strlen(paths->bundle);
  if (length + 5 > capacity) return false;
  memcpy(out, paths->bundle, length);
  memcpy(out + length, ".new", 5);
  return true;
}

static void log_linef(const struct lina_host *host, const char *format, const char *detail) {
  char line[256];
  snprintf(line, sizeof(line), format, detail == NULL ? "" : detail);
  host->log_line(host->context, line);
}

// What a failed attempt leaves behind, in an order that keeps the unlock path
// working: the rule first, then whatever the rule no longer names.
static void cleanup(const struct lina_host *host, const struct lina_paths *paths, struct attempt *attempt) {
  char staged[LINA_PATH_MAX];
  if (staged_bundle(paths, staged, sizeof(staged))) host->remove_bundle(host->context, staged);
  bool restored = true;
  if (attempt->rule) {
    // Restore while the bundle still exists, so no moment leaves the rule
    // naming a plug-in that is already gone.
    restored = lina_restore_backup(host, paths, "install-aborted", NULL) == 0;
  }
  // The right and the bundle go only when the rule is readable and no longer
  // names LINA's right. An unreadable database could still have the rule
  // naming it, and an earlier install's rule still needs both.
  struct lina_right current;
  bool keep = host->read_right(host->context, LINA_RIGHT_SCREENSAVER, &current) != 0
      || lina_rule_names_lina(&current);
  if (attempt->rights && !keep) {
    host->remove_right(host->context, LINA_RIGHT_UNLOCK);
    host->remove_right(host->context, LINA_RIGHT_SELFTEST);
  }
  if (attempt->bundle && !keep) host->remove_bundle(host->context, paths->bundle);
  if (attempt->guard) host->remove_guard(host->context, paths->guard, paths->guard_plist);
  if (attempt->enabled) host->remove_file(host->context, paths->enabled);
  if (attempt->pending) host->remove_file(host->context, paths->pending);
  if (attempt->measured) host->remove_file(host->context, paths->measured);
  // A backup that could not be restored is the only copy of the earlier rule;
  // it stays for the guard or a later attempt.
  if (attempt->backup && (!attempt->rule || restored)) {
    host->remove_file(host->context, paths->backup);
  }
  host->log_line(host->context, keep
      ? "the rule still needs LINA's right and plug-in, so they stay"
      : "nothing of this attempt is left");
}

static struct lina_outcome fail(const struct lina_host *host, const struct lina_paths *paths,
                                struct attempt *attempt, int code, const char *reason) {
  cleanup(host, paths, attempt);
  log_linef(host, "install stopped: %s", reason);
  return (struct lina_outcome){code, reason};
}

static bool socket_is_valid(const char *socket) {
  if (socket == NULL || socket[0] != '/') return false;
  size_t length = strlen(socket);
  if (length == 0 || length >= 104) return false;
  for (const char *cursor = socket; *cursor != '\0'; cursor++) {
    if ((unsigned char)*cursor < 0x20 || *cursor == '\n' || *cursor == '=') return false;
  }
  return true;
}

static bool measured_contains(const struct lina_host *host, const char *path, const char *build) {
  static char text[4096];
  size_t length = 0;
  if (host->read_file(host->context, path, text, sizeof(text), &length) != 1) return false;
  text[length] = '\0';
  for (char *line = strtok(text, "\n"); line != NULL; line = strtok(NULL, "\n")) {
    if (strcmp(line, build) == 0) return true;
  }
  return false;
}

static int copy_measured(const struct lina_host *host, const struct lina_paths *paths, const char *source) {
  static char text[4096];
  size_t length = 0;
  if (host->read_file(host->context, source, text, sizeof(text), &length) != 1) return -1;
  return host->write_file(host->context, paths->measured, text, length);
}

struct lina_outcome lina_install(const struct lina_host *host, const struct lina_paths *paths,
                                 const struct lina_install_input *input) {
  struct attempt attempt;
  memset(&attempt, 0, sizeof(attempt));
  // Nothing is touched before the input is known good: a rejected invocation
  // must not change an install that is already there.
  if (!socket_is_valid(input->broker_socket) || input->person_uid == 0
      || input->deadline_ms < 1 || input->deadline_ms > 500) {
    return (struct lina_outcome){LINA_INSTALL_STATE, "input"};
  }
  if (!host->is_apple_silicon(host->context)) {
    return fail(host, paths, &attempt, LINA_INSTALL_DEVICE, "not-apple-silicon");
  }
  char build[64];
  if (host->measure_build(host->context, build, sizeof(build)) != 1) {
    return fail(host, paths, &attempt, LINA_INSTALL_BUILD, "build-unknown");
  }
  // The release's own list counts for this install, so a first install on a
  // measured build does not depend on a list written by an earlier one.
  bool measured = input->measured_source != NULL
      ? measured_contains(host, input->measured_source, build)
      : host->build_is_measured(host->context, build);
  if (!host->eval_test_mac(host->context) && !measured) {
    return fail(host, paths, &attempt, LINA_INSTALL_BUILD, "build-unmeasured");
  }
  struct lina_right rule;
  if (host->read_right(host->context, LINA_RIGHT_SCREENSAVER, &rule) != 0) {
    return fail(host, paths, &attempt, LINA_INSTALL_RULE, "rule-unreadable");
  }
  if (!lina_rule_is_stock(&rule) && !lina_rule_is_lina(&rule)) {
    return fail(host, paths, &attempt, LINA_INSTALL_RULE, lina_rule_shape(&rule));
  }
  if (host->make_dir(host->context, paths->state_dir) != 0) {
    return fail(host, paths, &attempt, LINA_INSTALL_STATE, "state-dir");
  }
  // The backup records the rule that was there before any LINA install, so only
  // a stock rule is written into it. A re-install over LINA's own rule keeps the
  // earlier backup; when there is none, a restore falls back to the stock rule.
  // Either way a later uninstall never restores a rule that names a right it is
  // about to remove.
  if (lina_rule_is_stock(&rule)) {
    if (lina_backup_write(host, paths, &rule, build) != 0) {
      return fail(host, paths, &attempt, LINA_INSTALL_BACKUP, "backup");
    }
    attempt.backup = true;
  }
  if (input->measured_source != NULL) {
    if (copy_measured(host, paths, input->measured_source) != 0) {
      return fail(host, paths, &attempt, LINA_INSTALL_STATE, "measured-list");
    }
    attempt.measured = true;
  }
  // The new bundle is staged beside the installed one and only swapped in once
  // its signature checks, so a failed install never leaves a half-copied
  // bundle under the name the rule uses.
  char staged[LINA_PATH_MAX];
  if (!staged_bundle(paths, staged, sizeof(staged))) {
    return fail(host, paths, &attempt, LINA_INSTALL_STATE, "path");
  }
  host->remove_bundle(host->context, staged);
  if (host->copy_bundle(host->context, input->bundle_source, staged) != 0) {
    return fail(host, paths, &attempt, LINA_INSTALL_BUNDLE, "bundle-copy");
  }
  attempt.bundle = true;
  int verified = host->verify_code(host->context, staged);
  if (verified != 1) {
    return fail(host, paths, &attempt, LINA_INSTALL_SIGNATURE,
                verified == 0 ? "signature-invalid" : "signature-unreadable");
  }
  if (host->exists(host->context, paths->bundle) == 1
      && host->remove_bundle(host->context, paths->bundle) != 0) {
    return fail(host, paths, &attempt, LINA_INSTALL_BUNDLE, "bundle-replace");
  }
  if (host->rename_path(host->context, staged, paths->bundle) != 0) {
    return fail(host, paths, &attempt, LINA_INSTALL_BUNDLE, "bundle-copy");
  }
  if (input->guard_binary != NULL && input->guard_plist != NULL) {
    if (host->install_guard(host->context, input->guard_binary, input->guard_plist) != 0) {
      return fail(host, paths, &attempt, LINA_INSTALL_GUARD, "guard-install");
    }
    attempt.guard = true;
  }
  struct lina_right unlock;
  struct lina_right selftest;
  lina_right_unlock(&unlock);
  lina_right_selftest(&selftest);
  if (host->write_right(host->context, LINA_RIGHT_UNLOCK, &unlock) != 0
      || host->write_right(host->context, LINA_RIGHT_SELFTEST, &selftest) != 0) {
    return fail(host, paths, &attempt, LINA_INSTALL_RIGHT, "right");
  }
  attempt.rights = true;
  int self_test = host->evaluate_right(host->context, LINA_RIGHT_SELFTEST, input->deadline_ms);
  if (self_test != 0) {
    return fail(host, paths, &attempt, LINA_INSTALL_SELFTEST,
                self_test == 1 ? "self-test-allowed" : "self-test-unavailable");
  }
  struct lina_right lina_rule;
  lina_rule_lina(&lina_rule);
  if (host->write_right(host->context, LINA_RIGHT_SCREENSAVER, &lina_rule) != 0) {
    attempt.rule = true;
    return fail(host, paths, &attempt, LINA_INSTALL_READBACK, "rule-write");
  }
  attempt.rule = true;
  struct lina_right read_back;
  if (host->read_right(host->context, LINA_RIGHT_SCREENSAVER, &read_back) != 0
      || !lina_right_equal_ignoring_timestamps(&read_back, &lina_rule)) {
    return fail(host, paths, &attempt, LINA_INSTALL_READBACK, "read-back-mismatch");
  }
  if (lina_enabled_write(host, paths, input->broker_socket, input->person_uid, input->deadline_ms) != 0) {
    return fail(host, paths, &attempt, LINA_INSTALL_STATE, "enabled");
  }
  attempt.enabled = true;
  struct lina_pending pending = {host->monotonic_ms(host->context), 0};
  if (lina_pending_write(host, paths, &pending) != 0) {
    return fail(host, paths, &attempt, LINA_INSTALL_STATE, "pending");
  }
  attempt.pending = true;
  host->log_line(host->context, "installed; waiting for one manual lock and unlock");
  return (struct lina_outcome){LINA_INSTALL_OK, NULL};
}

int lina_uninstall(const struct lina_host *host, const struct lina_paths *paths, struct lina_outcome *outcome) {
  *outcome = (struct lina_outcome){LINA_UNINSTALL_OK, NULL};
  // Turning the feature off first makes the plug-in deny at once.
  host->remove_file(host->context, paths->enabled);
  host->remove_file(host->context, paths->pending);
  struct lina_right rule;
  int read = host->read_right(host->context, LINA_RIGHT_SCREENSAVER, &rule);
  bool keep = false;
  if (read == 0) {
    if (lina_rule_is_lina(&rule)) {
      if (lina_restore_backup(host, paths, "uninstall", NULL) != 0) {
        *outcome = (struct lina_outcome){LINA_UNINSTALL_FAILED, "restore"};
        return outcome->code;
      }
    } else if (lina_rule_names_lina(&rule)) {
      // Someone else's rule names LINA's right. Removing the right or the
      // bundle would break their rule, so both stay and the report says why.
      keep = true;
    }
  } else if (read < 0) {
    *outcome = (struct lina_outcome){LINA_UNINSTALL_DEFERRED, "rule-unreadable"};
    return outcome->code;
  }
  if (keep) {
    host->remove_file(host->context, paths->pending);
    *outcome = (struct lina_outcome){LINA_UNINSTALL_DEFERRED, "rule-changed"};
    host->log_line(host->context, "uninstall left a rule someone else wrote alone; LINA's right and bundle stay");
    return outcome->code;
  }
  host->remove_right(host->context, LINA_RIGHT_UNLOCK);
  host->remove_right(host->context, LINA_RIGHT_SELFTEST);
  host->remove_bundle(host->context, paths->bundle);
  host->remove_guard(host->context, paths->guard, paths->guard_plist);
  host->remove_file(host->context, paths->backup);
  host->remove_file(host->context, paths->measured);
  host->log_line(host->context, "uninstalled");
  return outcome->code;
}
