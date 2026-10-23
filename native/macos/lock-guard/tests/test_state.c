// SPDX-License-Identifier: Apache-2.0
// The backup file, the pending record and the enabled marker.
#include <string.h>

#include "install.h"
#include "state.h"
#include "test.h"
#include "fake_host.h"

void test_state(void) {
  struct fake_host fake;
  struct lina_paths paths;
  fake_host_init(&fake);
  lina_paths_prefix(&paths, "/tmp/lina-test");

  struct lina_right stock;
  lina_rule_stock(&stock);
  CHECK(lina_backup_write(&fake.host, &paths, &stock, "25A354") == 0);
  struct lina_backup backup;
  CHECK(lina_backup_read(&fake.host, &paths, &backup) == 1);
  CHECK_STR(backup.build, "25A354");
  CHECK(lina_right_equal(&backup.rule, &stock));

  // A backup that does not match its own hash is unusable, never restored.
  struct fake_file *file = NULL;
  for (int i = 0; i < FAKE_FILES; i++) {
    if (fake.files[i].present && strcmp(fake.files[i].path, paths.backup) == 0) file = &fake.files[i];
  }
  CHECK(file != NULL);
  char original[FAKE_FILE_BYTES];
  size_t original_length = file->length;
  memcpy(original, file->content, original_length);
  const char *needle = "class s rule";
  char *found = strstr(file->content, needle);
  CHECK(found != NULL);
  found[0] = 'k';  // `klass s rule`
  CHECK(lina_backup_read(&fake.host, &paths, &backup) == -1);

  memcpy(file->content, original, original_length);
  file->length = original_length;
  CHECK(lina_backup_read(&fake.host, &paths, &backup) == 1);
  // The build is covered by the hash too.
  found = strstr(file->content, "25A354");
  CHECK(found != NULL);
  found[0] = '2';
  found[5] = '5';
  CHECK(lina_backup_read(&fake.host, &paths, &backup) == -1);

  // Absent is absent, not an error.
  fake_host_init(&fake);
  lina_paths_prefix(&paths, "/tmp/lina-test");
  CHECK(lina_backup_read(&fake.host, &paths, &backup) == 0);

  struct lina_pending pending = {0};
  CHECK(lina_pending_read(&fake.host, &paths, &pending) == 0);
  CHECK(lina_pending_write(&fake.host, &paths, &pending) == 0);
  CHECK(lina_pending_read(&fake.host, &paths, &pending) == 1);
  CHECK(pending.lock_ms == 0);
  pending.lock_ms = 123456;
  CHECK(lina_pending_write(&fake.host, &paths, &pending) == 0);
  struct lina_pending again = {0};
  CHECK(lina_pending_read(&fake.host, &paths, &again) == 1);
  CHECK(again.lock_ms == 123456);

  // A pending record from an earlier format is not read as if it were this one.
  fake_file_set(&fake, paths.pending, "lina-lock-pending 1\nlock_ms 0\n");
  CHECK(lina_pending_read(&fake.host, &paths, &again) == -1);

  CHECK(lina_enabled_write(&fake.host, &paths, "/var/run/lina/broker.sock", 501, 500) == 0);
  CHECK(lina_enabled_present(&fake.host, &paths));
  char text[256];
  size_t length = 0;
  CHECK(fake.host.read_file(fake.host.context, paths.enabled, text, sizeof(text), &length) == 1);
  text[length] = '\0';
  CHECK_STR(text, "socket=/var/run/lina/broker.sock\nuid=501\ndeadline_ms=500\n");

  // Paths: one layout, whichever prefix the caller uses.
  struct lina_paths root;
  lina_paths_default(&root);
  CHECK_STR(root.state_dir, "/var/db/lina/locked-screen");
  CHECK_STR(root.bundle, "/Library/Security/SecurityAgentPlugins/LinaUnlock.bundle");
  CHECK_STR(root.guard_plist, "/Library/LaunchDaemons/com.lina.locked-screen.guard.plist");
}
