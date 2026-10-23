// SPDX-License-Identifier: Apache-2.0
// The guard: every trigger restores the rule and turns the feature off, and no
// path writes a rule other than the backup or the stock rule.
#include <string.h>

#include "guard.h"
#include "install.h"
#include "rule.h"
#include "state.h"
#include "test.h"
#include "fake_host.h"

static struct lina_paths paths;

static void installed(struct fake_host *fake) {
  fake_host_init(fake);
  lina_paths_prefix(&paths, "/tmp/lina-test");
  fake->now_ms = 1000;
  struct lina_install_input input = {
      .bundle_source = "/release/LinaUnlock.bundle",
      .broker_socket = "/var/run/lina/broker.sock",
      .person_uid = 501,
      .deadline_ms = 500,
  };
  CHECK(lina_install(&fake->host, &paths, &input).code == LINA_INSTALL_OK);
  fake->write_count = 0;
}

// Every rule the guard writes must be the backup or the stock rule.
static void only_backup_or_stock(struct fake_host *fake, const struct lina_right *backup) {
  struct lina_right stock;
  lina_rule_stock(&stock);
  for (int i = 0; i < fake->write_count; i++) {
    CHECK_STR(fake->writes[i].name, LINA_RIGHT_SCREENSAVER);
    struct lina_right written;
    CHECK(lina_right_parse(fake->writes[i].text, strlen(fake->writes[i].text), &written));
    CHECK(lina_right_equal(&written, &stock) || lina_right_equal(&written, backup));
    CHECK(!lina_rule_is_lina(&written));
  }
}

static void restored(struct fake_host *fake, const struct lina_right *expected) {
  struct fake_right *slot = fake_right_find(fake, LINA_RIGHT_SCREENSAVER);
  CHECK(slot != NULL && slot->present);
  CHECK(lina_right_equal_ignoring_timestamps(&slot->definition, expected));
  CHECK(!lina_enabled_present(&fake->host, &paths));
}

void test_guard(void) {
  struct lina_right stock;
  lina_rule_stock(&stock);

  // Nothing installed: the guard is idle and writes nothing.
  struct fake_host idle;
  fake_host_init(&idle);
  lina_paths_prefix(&paths, "/tmp/lina-test");
  struct lina_guard_report report = lina_guard_tick(&idle.host, &paths, 1000);
  CHECK(report.action == LINA_GUARD_IDLE);
  CHECK(idle.write_count == 0);

  // Installed and healthy: still idle.
  struct fake_host healthy;
  installed(&healthy);
  report = lina_guard_tick(&healthy.host, &paths, 2000);
  CHECK(report.action == LINA_GUARD_IDLE);
  CHECK(healthy.write_count == 0);

  // The pending check: a lock is recorded, the install stays pending inside two
  // minutes, and the verified unlock clears it.
  struct fake_host pending;
  installed(&pending);
  pending.locked = true;
  report = lina_guard_tick(&pending.host, &paths, 10000);
  CHECK(report.action == LINA_GUARD_IDLE);
  struct lina_pending record;
  CHECK(lina_pending_read(&pending.host, &paths, &record) == 1);
  CHECK(record.lock_ms == 10000);
  report = lina_guard_tick(&pending.host, &paths, 10000 + 119999);
  CHECK(report.action == LINA_GUARD_IDLE);
  CHECK(pending.write_count == 0);
  pending.locked = false;
  report = lina_guard_tick(&pending.host, &paths, 10000 + 200000);
  CHECK(report.action == LINA_GUARD_VERIFIED);
  CHECK(pending.write_count == 0);
  CHECK(lina_pending_read(&pending.host, &paths, &record) == 0);

  // A record written before a restart is reset, not treated as a lock from the
  // future.
  struct fake_host restarted;
  installed(&restarted);
  struct lina_pending stale = {restarted.now_ms + 5000, restarted.now_ms + 4000};
  CHECK(lina_pending_write(&restarted.host, &paths, &stale) == 0);
  report = lina_guard_tick(&restarted.host, &paths, restarted.now_ms + 10);
  CHECK(report.action == LINA_GUARD_IDLE);
  CHECK(restarted.write_count == 0);
  struct lina_pending reset;
  CHECK(lina_pending_read(&restarted.host, &paths, &reset) == 1);
  CHECK(reset.lock_ms == 0);
  restarted.locked = true;
  CHECK(lina_guard_tick(&restarted.host, &paths, restarted.now_ms + 20).action == LINA_GUARD_IDLE);
  CHECK(lina_pending_read(&restarted.host, &paths, &reset) == 1);
  CHECK(reset.lock_ms == restarted.now_ms + 20);
  restarted.locked = false;
  CHECK(lina_guard_tick(&restarted.host, &paths, restarted.now_ms + 30).action == LINA_GUARD_VERIFIED);

  // No manual unlock within two minutes of the lock restores the backup.
  struct fake_host timeout;
  installed(&timeout);
  timeout.locked = true;
  CHECK(lina_guard_tick(&timeout.host, &paths, 5000).action == LINA_GUARD_IDLE);
  report = lina_guard_tick(&timeout.host, &paths, 5000 + LINA_VERIFY_WINDOW_MS);
  CHECK(report.action == LINA_GUARD_RESTORED);
  CHECK_STR(report.reason, "verify-timeout");
  restored(&timeout, &stock);
  only_backup_or_stock(&timeout, &stock);

  // A restore leaves the rule equal to the backup in every key except its
  // timestamps, including keys LINA never wrote.
  struct fake_host exact;
  fake_host_init(&exact);
  lina_paths_prefix(&paths, "/tmp/lina-test");
  struct lina_right before_stock;
  lina_rule_stock(&before_stock);
  lina_right_set_string(&before_stock, "comment", "set by the fleet");
  fake_right_set(&exact, LINA_RIGHT_SCREENSAVER, &before_stock);
  struct lina_install_input exact_input = {
      .bundle_source = "/release/LinaUnlock.bundle",
      .broker_socket = "/var/run/lina/broker.sock",
      .person_uid = 501,
      .deadline_ms = 500,
  };
  CHECK(lina_install(&exact.host, &paths, &exact_input).code == LINA_INSTALL_OK);
  exact.write_count = 0;
  CHECK(lina_guard_restore(&exact.host, &paths, "rollback").action == LINA_GUARD_RESTORED);
  struct fake_right *restored_rule = fake_right_find(&exact, LINA_RIGHT_SCREENSAVER);
  CHECK(restored_rule != NULL);
  CHECK(lina_right_equal_ignoring_timestamps(&restored_rule->definition, &before_stock));
  CHECK(lina_right_get(&restored_rule->definition, "created") != NULL);
  CHECK(lina_right_get(&restored_rule->definition, "modified") != NULL);
  only_backup_or_stock(&exact, &before_stock);

  // The plugin folder changed and the bundle is gone.
  struct fake_host missing;
  installed(&missing);
  missing.bundle_present = false;
  report = lina_guard_tick(&missing.host, &paths, 20000);
  CHECK(report.action == LINA_GUARD_RESTORED);
  CHECK_STR(report.reason, "bundle-missing");
  restored(&missing, &stock);
  only_backup_or_stock(&missing, &stock);

  // The bundle is there and its signature is invalid.
  struct fake_host unsigned_bundle;
  installed(&unsigned_bundle);
  unsigned_bundle.verify_result = 0;
  report = lina_guard_tick(&unsigned_bundle.host, &paths, 20000);
  CHECK(report.action == LINA_GUARD_RESTORED);
  CHECK_STR(report.reason, "signature-invalid");
  restored(&unsigned_bundle, &stock);
  only_backup_or_stock(&unsigned_bundle, &stock);

  // The signature cannot be checked at all.
  struct fake_host unchecked;
  installed(&unchecked);
  unchecked.verify_result = -1;
  report = lina_guard_tick(&unchecked.host, &paths, 20000);
  CHECK(report.action == LINA_GUARD_RESTORED);
  CHECK_STR(report.reason, "signature-unreadable");

  // LINA's unlock right is gone while the rule names it.
  struct fake_host right_missing;
  installed(&right_missing);
  right_missing.host.remove_right(right_missing.host.context, LINA_RIGHT_UNLOCK);
  report = lina_guard_tick(&right_missing.host, &paths, 20000);
  CHECK(report.action == LINA_GUARD_RESTORED);
  CHECK_STR(report.reason, "right-missing");

  // The rule differs from what LINA wrote.
  struct fake_host changed;
  installed(&changed);
  struct lina_right other;
  lina_right_clear(&other);
  lina_right_set_string(&other, "class", "rule");
  lina_right_set_string(&other, "rule", "authenticate-session-owner-or-admin");
  fake_right_set(&changed, LINA_RIGHT_SCREENSAVER, &other);
  report = lina_guard_tick(&changed.host, &paths, 20000);
  CHECK(report.action == LINA_GUARD_RESTORED);
  CHECK_STR(report.reason, "rule-changed");
  restored(&changed, &stock);
  only_backup_or_stock(&changed, &stock);

  // The build is not one the eval passed.
  struct fake_host unmeasured;
  installed(&unmeasured);
  snprintf(unmeasured.build, sizeof(unmeasured.build), "26B100");
  report = lina_guard_tick(&unmeasured.host, &paths, 20000);
  CHECK(report.action == LINA_GUARD_RESTORED);
  CHECK_STR(report.reason, "build-unmeasured");
  restored(&unmeasured, &stock);
  only_backup_or_stock(&unmeasured, &stock);

  // The eval's test Mac waives the build check for the guard too.
  struct fake_host waived;
  installed(&waived);
  snprintf(waived.build, sizeof(waived.build), "26B100");
  waived.eval_test_mac = true;
  CHECK(lina_guard_tick(&waived.host, &paths, 20000).action == LINA_GUARD_IDLE);

  // An unusable backup falls back to the stock rule.
  struct fake_host damaged;
  installed(&damaged);
  struct fake_right *slot = fake_right_find(&damaged, LINA_RIGHT_SCREENSAVER);
  struct lina_right before = slot->definition;
  for (int i = 0; i < FAKE_FILES; i++) {
    if (damaged.files[i].present && strcmp(damaged.files[i].path, paths.backup) == 0) {
      damaged.files[i].content[0] = 'x';
    }
  }
  (void)before;
  damaged.bundle_present = false;
  report = lina_guard_tick(&damaged.host, &paths, 20000);
  CHECK(report.action == LINA_GUARD_RESTORED);
  CHECK(report.used_stock);
  restored(&damaged, &stock);
  only_backup_or_stock(&damaged, &stock);
}
