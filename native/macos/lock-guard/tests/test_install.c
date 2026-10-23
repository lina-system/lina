// SPDX-License-Identifier: Apache-2.0
// Install and uninstall against a fake authorization database.
#include <string.h>

#include "install.h"
#include "rule.h"
#include "state.h"
#include "test.h"
#include "fake_host.h"

static struct lina_paths paths;
static struct lina_install_input input;

static void setup(struct fake_host *fake) {
  fake_host_init(fake);
  lina_paths_prefix(&paths, "/tmp/lina-test");
  input.bundle_source = "/release/LinaUnlock.bundle";
  input.guard_binary = "/release/com.lina.locked-screen.guard";
  input.guard_plist = "/release/com.lina.locked-screen.guard.plist";
  input.measured_source = NULL;
  input.broker_socket = "/var/run/lina/broker.sock";
  input.person_uid = 501;
  input.deadline_ms = 500;
}

static bool definitions_match(struct fake_host *fake, const char *name, const struct lina_right *expected) {
  struct fake_right *slot = fake_right_find(fake, name);
  if (slot == NULL || !slot->present) return false;
  return lina_right_equal_ignoring_timestamps(&slot->definition, expected);
}

static void install_writes_designed_definitions(void) {
  struct fake_host fake;
  setup(&fake);
  struct lina_outcome outcome = lina_install(&fake.host, &paths, &input);
  CHECK(outcome.code == LINA_INSTALL_OK);
  struct lina_right lina;
  struct lina_right unlock;
  lina_rule_lina(&lina);
  lina_right_unlock(&unlock);
  CHECK(definitions_match(&fake, LINA_RIGHT_SCREENSAVER, &lina));
  CHECK(definitions_match(&fake, LINA_RIGHT_UNLOCK, &unlock));
  CHECK(definitions_match(&fake, LINA_RIGHT_SELFTEST, &unlock));
  // The three rights are the only ones written.
  CHECK(fake.write_count == 3);
  CHECK_STR(fake.writes[0].name, LINA_RIGHT_UNLOCK);
  CHECK_STR(fake.writes[1].name, LINA_RIGHT_SELFTEST);
  CHECK_STR(fake.writes[2].name, LINA_RIGHT_SCREENSAVER);
  // Exactly the designed text for the rule: class rule, k-of-n 1, LINA's right
  // then the stock rule.
  CHECK(strstr(fake.writes[2].text, "class s rule\n") != NULL);
  CHECK(strstr(fake.writes[2].text, "k-of-n i 1\n") != NULL);
  CHECK(strstr(fake.writes[2].text, "rule a 2 com.lina.locked-screen.unlock use-login-window-ui\n") != NULL);
  CHECK(strstr(fake.writes[2].text, "tries") == NULL);

  struct lina_backup backup;
  CHECK(lina_backup_read(&fake.host, &paths, &backup) == 1);
  CHECK_STR(backup.build, "25A354");
  struct lina_right stock;
  lina_rule_stock(&stock);
  CHECK(lina_right_equal_ignoring_timestamps(&backup.rule, &stock));
  CHECK(fake.bundle_present);
  CHECK(fake.guard_installed == 1);
  CHECK(lina_enabled_present(&fake.host, &paths));

  struct lina_pending pending;
  CHECK(lina_pending_read(&fake.host, &paths, &pending) == 1);
  CHECK(pending.lock_ms == 0);
}

static void install_refuses_platform_sso(void) {
  struct fake_host fake;
  setup(&fake);
  struct lina_right psso;
  lina_right_clear(&psso);
  lina_right_set_string(&psso, "class", "rule");
  lina_right_set_int(&psso, "k-of-n", 2);
  const char *items[] = {"psso-screensaver", "authenticate-session-owner-or-admin"};
  lina_right_set_list(&psso, "rule", items, 2);
  fake_right_set(&fake, LINA_RIGHT_SCREENSAVER, &psso);

  struct lina_outcome outcome = lina_install(&fake.host, &paths, &input);
  CHECK(outcome.code == LINA_INSTALL_RULE);
  CHECK_STR(outcome.reason, "platform-sso");
  // Nothing was written, no right was added, no bundle was copied and the rule
  // is untouched.
  CHECK(fake.write_count == 0);
  CHECK(fake_right_find(&fake, LINA_RIGHT_UNLOCK) == NULL);
  CHECK(!fake.bundle_present);
  CHECK(definitions_match(&fake, LINA_RIGHT_SCREENSAVER, &psso));
  CHECK(!lina_enabled_present(&fake.host, &paths));
}

static void install_refuses_any_other_rule(void) {
  const char *rules[] = {"authenticate-session-owner-or-admin", "CryptoTokenKit:login",
                         "com.example.vendor:unlock,privileged", "managed-screensaver"};
  for (size_t i = 0; i < sizeof(rules) / sizeof(rules[0]); i++) {
    struct fake_host fake;
    setup(&fake);
    struct lina_right other;
    lina_right_clear(&other);
    lina_right_set_string(&other, "class", "rule");
    lina_right_set_string(&other, "rule", rules[i]);
    fake_right_set(&fake, LINA_RIGHT_SCREENSAVER, &other);
    struct lina_outcome outcome = lina_install(&fake.host, &paths, &input);
    CHECK(outcome.code == LINA_INSTALL_RULE);
    CHECK(fake.write_count == 0);
    CHECK(!fake.bundle_present);
    CHECK(definitions_match(&fake, LINA_RIGHT_SCREENSAVER, &other));
  }
}

static void install_accepts_an_earlier_lina_rule(void) {
  struct fake_host fake;
  setup(&fake);
  struct lina_right earlier;
  lina_rule_lina(&earlier);
  fake_right_set(&fake, LINA_RIGHT_SCREENSAVER, &earlier);
  struct lina_outcome outcome = lina_install(&fake.host, &paths, &input);
  CHECK(outcome.code == LINA_INSTALL_OK);
  struct lina_backup backup;
  CHECK(lina_backup_read(&fake.host, &paths, &backup) == 1);
  CHECK(lina_right_equal_ignoring_timestamps(&backup.rule, &earlier));

  // A re-install over an earlier LINA install swaps the verified bundle in
  // place of the installed one and writes the rule again.
  struct fake_host again;
  setup(&again);
  fake_right_set(&again, LINA_RIGHT_SCREENSAVER, &earlier);
  again.bundle_present = true;
  snprintf(again.bundle_path, sizeof(again.bundle_path), "%s", paths.bundle);
  again.write_count = 0;
  CHECK(lina_install(&again.host, &paths, &input).code == LINA_INSTALL_OK);
  CHECK(again.write_count == 3);
  CHECK(again.bundle_present);
  CHECK_STR(again.bundle_path, paths.bundle);
}

static void install_refuses_unmeasured_build(void) {
  struct fake_host fake;
  setup(&fake);
  snprintf(fake.build, sizeof(fake.build), "26B100");
  struct lina_outcome outcome = lina_install(&fake.host, &paths, &input);
  CHECK(outcome.code == LINA_INSTALL_BUILD);
  CHECK_STR(outcome.reason, "build-unmeasured");
  CHECK(fake.write_count == 0);
  CHECK(!fake.bundle_present);

  // The eval's test Mac waives the build check, and nothing else does.
  struct fake_host waived;
  setup(&waived);
  snprintf(waived.build, sizeof(waived.build), "26B100");
  waived.eval_test_mac = true;
  CHECK(lina_install(&waived.host, &paths, &input).code == LINA_INSTALL_OK);

  struct fake_host intel;
  setup(&intel);
  intel.apple_silicon = false;
  CHECK(lina_install(&intel.host, &paths, &input).code == LINA_INSTALL_DEVICE);
}

static void install_restores_when_read_back_differs(void) {
  struct fake_host fake;
  setup(&fake);
  // The database accepts the write but keeps the stock rule, so the read-back
  // differs from what LINA wrote.
  fake.normalise_rule = true;
  struct lina_outcome outcome = lina_install(&fake.host, &paths, &input);
  CHECK(outcome.code == LINA_INSTALL_READBACK);
  CHECK_STR(outcome.reason, "read-back-mismatch");
  struct lina_right stock;
  lina_rule_stock(&stock);
  CHECK(definitions_match(&fake, LINA_RIGHT_SCREENSAVER, &stock));
  CHECK(!fake.bundle_present);
  CHECK(fake_right_find(&fake, LINA_RIGHT_UNLOCK) == NULL);
  CHECK(!lina_enabled_present(&fake.host, &paths));
}

static void install_cleans_up_after_failed_self_test(void) {
  struct fake_host fake;
  setup(&fake);
  fake.evaluate_result = 1;  // the self-test allowed, which must never happen
  struct lina_outcome outcome = lina_install(&fake.host, &paths, &input);
  CHECK(outcome.code == LINA_INSTALL_SELFTEST);
  CHECK_STR(outcome.reason, "self-test-allowed");
  CHECK(!fake.bundle_present);
  CHECK(fake_right_find(&fake, LINA_RIGHT_UNLOCK) == NULL);
  CHECK(fake_right_find(&fake, LINA_RIGHT_SELFTEST) == NULL);
  struct lina_right stock;
  lina_rule_stock(&stock);
  CHECK(definitions_match(&fake, LINA_RIGHT_SCREENSAVER, &stock));
  CHECK(!lina_enabled_present(&fake.host, &paths));

  struct fake_host unanswered;
  setup(&unanswered);
  unanswered.evaluate_result = -1;
  CHECK(lina_install(&unanswered.host, &paths, &input).code == LINA_INSTALL_SELFTEST);
  CHECK(!unanswered.bundle_present);
}

static void install_stops_on_bad_signature(void) {
  struct fake_host fake;
  setup(&fake);
  fake.verify_result = 0;
  struct lina_outcome outcome = lina_install(&fake.host, &paths, &input);
  CHECK(outcome.code == LINA_INSTALL_SIGNATURE);
  CHECK_STR(outcome.reason, "signature-invalid");
  CHECK(!fake.bundle_present);
  CHECK(fake.write_count == 0);
}

static void install_keeps_an_earlier_install_working(void) {
  // A re-install that fails still leaves the earlier rule, its right and its
  // plug-in in place, so the person's unlock keeps working.
  struct fake_host fake;
  setup(&fake);
  struct lina_right earlier;
  lina_rule_lina(&earlier);
  fake_right_set(&fake, LINA_RIGHT_SCREENSAVER, &earlier);
  fake.bundle_present = true;
  snprintf(fake.bundle_path, sizeof(fake.bundle_path), "%s", paths.bundle);
  struct lina_right unlock;
  lina_right_unlock(&unlock);
  fake_right_set(&fake, LINA_RIGHT_UNLOCK, &unlock);
  fake_right_set(&fake, LINA_RIGHT_SELFTEST, &unlock);
  fake.evaluate_result = 1;  // the self-test allows, so the install stops

  struct lina_outcome outcome = lina_install(&fake.host, &paths, &input);
  CHECK(outcome.code == LINA_INSTALL_SELFTEST);
  CHECK(definitions_match(&fake, LINA_RIGHT_SCREENSAVER, &earlier));
  CHECK(definitions_match(&fake, LINA_RIGHT_UNLOCK, &unlock));
  CHECK(fake.bundle_present);
  CHECK_STR(fake.bundle_path, paths.bundle);
  CHECK(!lina_enabled_present(&fake.host, &paths));

  // A bundle that never copies leaves the earlier one alone too.
  struct fake_host copy_failed;
  setup(&copy_failed);
  fake_right_set(&copy_failed, LINA_RIGHT_SCREENSAVER, &earlier);
  copy_failed.bundle_present = true;
  snprintf(copy_failed.bundle_path, sizeof(copy_failed.bundle_path), "%s", paths.bundle);
  copy_failed.copy_status = -1;
  outcome = lina_install(&copy_failed.host, &paths, &input);
  CHECK(outcome.code == LINA_INSTALL_BUNDLE);
  CHECK_STR(outcome.reason, "bundle-copy");
  CHECK(definitions_match(&copy_failed, LINA_RIGHT_SCREENSAVER, &earlier));
  CHECK(copy_failed.bundle_present);
  CHECK_STR(copy_failed.bundle_path, paths.bundle);
}

void test_install(void) {
  install_writes_designed_definitions();
  install_refuses_platform_sso();
  install_refuses_any_other_rule();
  install_accepts_an_earlier_lina_rule();
  install_refuses_unmeasured_build();
  install_cleans_up_after_failed_self_test();
  install_stops_on_bad_signature();
  install_keeps_an_earlier_install_working();
  install_restores_when_read_back_differs();
}
