// SPDX-License-Identifier: Apache-2.0
// Uninstall ordering: no moment leaves a rule naming a missing plug-in.
#include <string.h>

#include "install.h"
#include "rule.h"
#include "state.h"
#include "test.h"
#include "fake_host.h"

static struct lina_paths paths;

static void installed(struct fake_host *fake) {
  fake_host_init(fake);
  lina_paths_prefix(&paths, "/tmp/lina-test");
  struct lina_install_input input = {
      .bundle_source = "/release/LinaUnlock.bundle",
      .guard_binary = "/release/com.lina.locked-screen.guard",
      .guard_plist = "/release/com.lina.locked-screen.guard.plist",
      .broker_socket = "/var/run/lina/broker.sock",
      .person_uid = 501,
      .deadline_ms = 500,
  };
  struct lina_outcome outcome = lina_install(&fake->host, &paths, &input);
  CHECK(outcome.code == LINA_INSTALL_OK);
}

static bool rule_is(struct fake_host *fake, const struct lina_right *expected) {
  struct fake_right *slot = fake_right_find(fake, LINA_RIGHT_SCREENSAVER);
  return slot != NULL && slot->present && lina_right_equal_ignoring_timestamps(&slot->definition, expected);
}

void test_uninstall(void) {
  // The normal case: the rule returns to the backup, then the right, the
  // bundle and the state go, in that order.
  struct fake_host fake;
  installed(&fake);
  fake.write_count = 0;
  struct lina_outcome outcome;
  CHECK(lina_uninstall(&fake.host, &paths, &outcome) == LINA_UNINSTALL_OK);
  CHECK(outcome.code == LINA_UNINSTALL_OK);
  struct lina_right stock;
  lina_rule_stock(&stock);
  CHECK(rule_is(&fake, &stock));
  CHECK(fake.write_count == 1);
  CHECK_STR(fake.writes[0].name, LINA_RIGHT_SCREENSAVER);
  CHECK(fake_right_find(&fake, LINA_RIGHT_UNLOCK) == NULL);
  CHECK(fake_right_find(&fake, LINA_RIGHT_SELFTEST) == NULL);
  CHECK(!fake.bundle_present);
  CHECK(!fake.guard_installed);
  CHECK(!lina_enabled_present(&fake.host, &paths));
  struct lina_backup backup;
  CHECK(lina_backup_read(&fake.host, &paths, &backup) == 0);

  // A rule someone else changed is left alone and reported, but LINA's parts
  // still go because their rule does not name them.
  struct fake_host changed;
  installed(&changed);
  struct lina_right other;
  lina_right_clear(&other);
  lina_right_set_string(&other, "class", "rule");
  lina_right_set_string(&other, "rule", "authenticate-session-owner-or-admin");
  fake_right_set(&changed, LINA_RIGHT_SCREENSAVER, &other);
  changed.write_count = 0;
  CHECK(lina_uninstall(&changed.host, &paths, &outcome) == LINA_UNINSTALL_OK);
  CHECK(rule_is(&changed, &other));
  CHECK(changed.write_count == 0);
  CHECK(!changed.bundle_present);
  CHECK(fake_right_find(&changed, LINA_RIGHT_UNLOCK) == NULL);

  // A rule someone else wrote that still names LINA's right keeps the bundle
  // and the right, so the rule never names a missing plug-in.
  struct fake_host naming;
  installed(&naming);
  struct lina_right foreign;
  lina_right_clear(&foreign);
  lina_right_set_string(&foreign, "class", "rule");
  lina_right_set_int(&foreign, "k-of-n", 2);
  const char *items[] = {LINA_RIGHT_UNLOCK, "psso-screensaver"};
  lina_right_set_list(&foreign, "rule", items, 2);
  fake_right_set(&naming, LINA_RIGHT_SCREENSAVER, &foreign);
  naming.write_count = 0;
  CHECK(lina_uninstall(&naming.host, &paths, &outcome) == LINA_UNINSTALL_DEFERRED);
  CHECK_STR(outcome.reason, "rule-changed");
  CHECK(rule_is(&naming, &foreign));
  CHECK(naming.write_count == 0);
  CHECK(naming.bundle_present);
  CHECK(fake_right_find(&naming, LINA_RIGHT_UNLOCK) != NULL);
  // The feature is off even then, so the plug-in denies at once.
  CHECK(!lina_enabled_present(&naming.host, &paths));

  // An unreadable rule also keeps the parts: the guard and a later uninstall
  // can still put it right.
  struct fake_host unreadable;
  installed(&unreadable);
  unreadable.read_right_status = -1;
  CHECK(lina_uninstall(&unreadable.host, &paths, &outcome) == LINA_UNINSTALL_DEFERRED);
  CHECK_STR(outcome.reason, "rule-unreadable");
  CHECK(unreadable.bundle_present);
  CHECK(fake_right_find(&unreadable, LINA_RIGHT_UNLOCK) != NULL);
}
