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
};

static void log_linef(const struct lina_host *host, const char *format, const char *detail) {
  char line[256];
  snprintf(line, sizeof(line), format, detail == NULL ? "" : detail);
  host->log_line(host->context, line);
}

static void cleanup(const struct lina_host *host, const struct lina_paths *paths, struct attempt *attempt) {
  if (attempt->rule) {
    // Restore while the bundle still exists, so no moment leaves the rule
    // naming a plug-in that is already gone.
    lina_restore_backup(host, paths, "install-aborted", NULL);
  }
  if (attempt->rights) {
    host->remove_right(host->context, LINA_RIGHT_UNLOCK);
    host->remove_right(host->context, LINA_RIGHT_SELFTEST);
  }
  if (attempt->bundle) host->remove_bundle(host->context, paths->bundle);
  if (attempt->guard) host->remove_guard(host->context, paths->guard, paths->guard_plist);
  host->remove_file(host->context, paths->enabled);
  host->remove_file(host->context, paths->pending);
  host->remove_file(host->context, paths->measured);
  if (attempt->backup) host->remove_file(host->context, paths->backup);
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
  if (!socket_is_valid(input->broker_socket) || input->person_uid == 0
      || input->deadline_ms < 1 || input->deadline_ms > 500) {
    return fail(host, paths, &attempt, LINA_INSTALL_STATE, "input");
  }
  if (!host->is_apple_silicon(host->context)) {
    return fail(host, paths, &attempt, LINA_INSTALL_DEVICE, "not-apple-silicon");
  }
  char build[64];
  if (host->measure_build(host->context, build, sizeof(build)) != 1) {
    return fail(host, paths, &attempt, LINA_INSTALL_BUILD, "build-unknown");
  }
  if (!host->eval_test_mac(host->context) && !host->build_is_measured(host->context, build)) {
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
  if (lina_backup_write(host, paths, &rule, build) != 0) {
    return fail(host, paths, &attempt, LINA_INSTALL_BACKUP, "backup");
  }
  attempt.backup = true;
  if (input->measured_source != NULL && copy_measured(host, paths, input->measured_source) != 0) {
    return fail(host, paths, &attempt, LINA_INSTALL_STATE, "measured-list");
  }
  if (host->copy_bundle(host->context, input->bundle_source, paths->bundle) != 0) {
    return fail(host, paths, &attempt, LINA_INSTALL_BUNDLE, "bundle-copy");
  }
  attempt.bundle = true;
  int verified = host->verify_code(host->context, paths->bundle);
  if (verified != 1) {
    return fail(host, paths, &attempt, LINA_INSTALL_SIGNATURE,
                verified == 0 ? "signature-invalid" : "signature-unreadable");
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
  struct lina_pending pending = {host->monotonic_ms(host->context), 0};
  if (lina_pending_write(host, paths, &pending) != 0) {
    return fail(host, paths, &attempt, LINA_INSTALL_STATE, "pending");
  }
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
