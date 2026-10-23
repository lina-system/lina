// SPDX-License-Identifier: Apache-2.0
#include "fake_host.h"

#include <stdio.h>
#include <string.h>

static struct fake_host *self(void *context) { return context; }

static struct fake_right *find_right(struct fake_host *fake, const char *name) {
  for (int i = 0; i < FAKE_RIGHTS; i++) {
    if (fake->rights[i].present && strcmp(fake->rights[i].name, name) == 0) return &fake->rights[i];
  }
  return NULL;
}

struct fake_right *fake_right_find(struct fake_host *fake, const char *name) { return find_right(fake, name); }

static struct fake_file *find_file(struct fake_host *fake, const char *path) {
  for (int i = 0; i < FAKE_FILES; i++) {
    if (fake->files[i].present && strcmp(fake->files[i].path, path) == 0) return &fake->files[i];
  }
  return NULL;
}

void fake_right_set(struct fake_host *fake, const char *name, const struct lina_right *definition) {
  struct fake_right *slot = find_right(fake, name);
  if (slot == NULL) {
    for (int i = 0; i < FAKE_RIGHTS; i++) {
      if (!fake->rights[i].present) {
        slot = &fake->rights[i];
        break;
      }
    }
  }
  slot->present = true;
  snprintf(slot->name, sizeof(slot->name), "%s", name);
  slot->definition = *definition;
}

static int read_right(void *context, const char *name, struct lina_right *out) {
  struct fake_host *fake = self(context);
  if (fake->read_right_status != 0) return fake->read_right_status;
  struct fake_right *slot = find_right(fake, name);
  if (slot == NULL) return 1;
  *out = slot->definition;
  return 0;
}

static int write_right(void *context, const char *name, const struct lina_right *right) {
  struct fake_host *fake = self(context);
  if (fake->write_right_status != 0) return fake->write_right_status;
  if (fake->write_count == FAKE_WRITES) return -1;
  snprintf(fake->writes[fake->write_count].name, sizeof(fake->writes[fake->write_count].name), "%s", name);
  if (lina_right_serialize(right, fake->writes[fake->write_count].text, LINA_RULE_TEXT_MAX) == 0) return -1;
  fake->write_count++;
  // The authorization database keeps its own timestamps; the definition it
  // stores carries them from then on.
  struct lina_right stored = *right;
  if (fake->normalise_rule && strcmp(name, LINA_RIGHT_SCREENSAVER) == 0) lina_rule_stock(&stored);
  lina_right_set_int(&stored, "created", (long long)(fake->now_ms / 1000));
  lina_right_set_int(&stored, "modified", (long long)(fake->now_ms / 1000));
  fake_right_set(fake, name, &stored);
  return 0;
}

static int remove_right(void *context, const char *name) {
  struct fake_host *fake = self(context);
  struct fake_right *slot = find_right(fake, name);
  if (slot == NULL) return 1;
  slot->present = false;
  return 0;
}

static int evaluate_right(void *context, const char *name, uint32_t deadline_ms) {
  struct fake_host *fake = self(context);
  (void)deadline_ms;
  struct fake_right *slot = find_right(fake, name);
  if (slot == NULL) return -1;
  return fake->evaluate_result;
}

static int copy_bundle(void *context, const char *source, const char *destination) {
  struct fake_host *fake = self(context);
  (void)source;
  if (fake->copy_status != 0) return fake->copy_status;
  snprintf(fake->bundle_text, sizeof(fake->bundle_text), "%s", destination);
  snprintf(fake->bundle_path, sizeof(fake->bundle_path), "%s", destination);
  fake->bundle_present = true;
  return 0;
}

static int rename_path(void *context, const char *from, const char *to) {
  struct fake_host *fake = self(context);
  if (!fake->bundle_present || strcmp(from, fake->bundle_path) != 0) return -1;
  snprintf(fake->bundle_path, sizeof(fake->bundle_path), "%s", to);
  return 0;
}

static int verify_code(void *context, const char *path) {
  struct fake_host *fake = self(context);
  if (!fake->bundle_present || strcmp(path, fake->bundle_path) != 0) return 0;
  return fake->verify_result;
}

static int remove_bundle(void *context, const char *path) {
  struct fake_host *fake = self(context);
  if (!fake->bundle_present || strcmp(path, fake->bundle_path) != 0) return 1;
  fake->bundle_present = false;
  return 0;
}

static int install_guard(void *context, const char *binary, const char *plist) {
  struct fake_host *fake = self(context);
  (void)binary;
  (void)plist;
  if (fake->guard_install_status != 0) return fake->guard_install_status;
  fake->guard_installed = 1;
  return 0;
}

static int remove_guard(void *context, const char *binary, const char *plist) {
  struct fake_host *fake = self(context);
  (void)binary;
  (void)plist;
  fake->guard_installed = 0;
  return 0;
}

static int read_file(void *context, const char *path, char *out, size_t capacity, size_t *length) {
  struct fake_host *fake = self(context);
  struct fake_file *file = find_file(fake, path);
  if (file == NULL) return 0;
  if (file->length > capacity) return -1;
  memcpy(out, file->content, file->length);
  *length = file->length;
  return 1;
}

static int write_file(void *context, const char *path, const void *data, size_t length) {
  struct fake_host *fake = self(context);
  if (length > FAKE_FILE_BYTES) return -1;
  if (strstr(path, "/pending") != NULL && fake->pending_write_status != 0) return fake->pending_write_status;
  struct fake_file *file = find_file(fake, path);
  if (file == NULL) {
    for (int i = 0; i < FAKE_FILES; i++) {
      if (!fake->files[i].present) {
        file = &fake->files[i];
        break;
      }
    }
  }
  if (file == NULL) return -1;
  file->present = true;
  snprintf(file->path, sizeof(file->path), "%s", path);
  memcpy(file->content, data, length);
  file->length = length;
  return 0;
}

static int remove_file(void *context, const char *path) {
  struct fake_host *fake = self(context);
  struct fake_file *file = find_file(fake, path);
  if (file == NULL) return 1;
  file->present = false;
  return 0;
}

static int exists(void *context, const char *path) {
  struct fake_host *fake = self(context);
  if (strstr(path, "LinaUnlock.bundle") != NULL) {
    return fake->bundle_present && strcmp(path, fake->bundle_path) == 0 ? 1 : 0;
  }
  return find_file(fake, path) != NULL ? 1 : 0;
}

static int make_dir(void *context, const char *path) {
  (void)context;
  (void)path;
  return 0;
}

static int measure_build(void *context, char *out, size_t capacity) {
  struct fake_host *fake = self(context);
  if (!fake->build_known) return 0;
  snprintf(out, capacity, "%s", fake->build);
  return 1;
}

static int is_apple_silicon(void *context) { return self(context)->apple_silicon ? 1 : 0; }

static int build_is_measured(void *context, const char *build) {
  struct fake_host *fake = self(context);
  for (int i = 0; i < fake->measured_count; i++) {
    if (strcmp(fake->measured[i], build) == 0) return 1;
  }
  return 0;
}

static int eval_test_mac(void *context) { return self(context)->eval_test_mac ? 1 : 0; }

static int session_locked(void *context, bool *locked) {
  struct fake_host *fake = self(context);
  if (fake->session_locked_status != 0) return fake->session_locked_status;
  *locked = fake->locked;
  return 0;
}

static uint64_t monotonic_ms(void *context) { return self(context)->now_ms; }

static void log_line(void *context, const char *line) {
  struct fake_host *fake = self(context);
  if (fake->log_count == FAKE_LOGS) return;
  snprintf(fake->logs[fake->log_count], sizeof(fake->logs[fake->log_count]), "%s", line);
  fake->log_count++;
}

void fake_file_set(struct fake_host *fake, const char *path, const char *content) {
  write_file(fake, path, content, strlen(content));
}

void fake_host_init(struct fake_host *fake) {
  memset(fake, 0, sizeof(*fake));
  fake->host.context = fake;
  fake->host.read_right = read_right;
  fake->host.write_right = write_right;
  fake->host.remove_right = remove_right;
  fake->host.evaluate_right = evaluate_right;
  fake->host.copy_bundle = copy_bundle;
  fake->host.verify_code = verify_code;
  fake->host.remove_bundle = remove_bundle;
  fake->host.rename_path = rename_path;
  fake->host.install_guard = install_guard;
  fake->host.remove_guard = remove_guard;
  fake->host.read_file = read_file;
  fake->host.write_file = write_file;
  fake->host.remove_file = remove_file;
  fake->host.exists = exists;
  fake->host.make_dir = make_dir;
  fake->host.measure_build = measure_build;
  fake->host.is_apple_silicon = is_apple_silicon;
  fake->host.build_is_measured = build_is_measured;
  fake->host.eval_test_mac = eval_test_mac;
  fake->host.session_locked = session_locked;
  fake->host.monotonic_ms = monotonic_ms;
  fake->host.log_line = log_line;
  fake->apple_silicon = true;
  fake->build_known = true;
  snprintf(fake->build, sizeof(fake->build), "25A354");
  snprintf(fake->measured[0], sizeof(fake->measured[0]), "25A354");
  fake->measured_count = 1;
  fake->verify_result = 1;
  fake->evaluate_result = 0;
  fake->now_ms = 1000;
  struct lina_right stock;
  lina_rule_stock(&stock);
  fake_right_set(fake, LINA_RIGHT_SCREENSAVER, &stock);
}
