// SPDX-License-Identifier: Apache-2.0
#include "state.h"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "sha256.h"

static void join(char *out, size_t capacity, const char *directory, const char *name) {
  snprintf(out, capacity, "%s/%s", directory, name);
}

void lina_paths_prefix(struct lina_paths *paths, const char *prefix) {
  snprintf(paths->state_dir, LINA_PATH_MAX, "%s/var/db/lina/locked-screen", prefix);
  join(paths->backup, LINA_PATH_MAX, paths->state_dir, "backup");
  join(paths->pending, LINA_PATH_MAX, paths->state_dir, "pending");
  join(paths->enabled, LINA_PATH_MAX, paths->state_dir, "enabled");
  join(paths->measured, LINA_PATH_MAX, paths->state_dir, "measured-builds");
  join(paths->eval_marker, LINA_PATH_MAX, paths->state_dir, "eval-test-mac");
  snprintf(paths->bundle, LINA_PATH_MAX, "%s/Library/Security/SecurityAgentPlugins/LinaUnlock.bundle", prefix);
  snprintf(paths->guard, LINA_PATH_MAX, "%s/Library/PrivilegedHelperTools/com.lina.locked-screen.guard",
           prefix);
  snprintf(paths->guard_plist, LINA_PATH_MAX, "%s/Library/LaunchDaemons/com.lina.locked-screen.guard.plist",
           prefix);
}

void lina_paths_default(struct lina_paths *paths) { lina_paths_prefix(paths, ""); }

static int read_text(const struct lina_host *host, const char *path, char *out, size_t capacity,
                     size_t *length) {
  int found = host->read_file(host->context, path, out, capacity, length);
  if (found != 1) return found;
  out[*length < capacity ? *length : capacity - 1] = '\0';
  return 1;
}

int lina_pending_read(const struct lina_host *host, const struct lina_paths *paths, struct lina_pending *out) {
  char text[128];
  size_t length = 0;
  int found = read_text(host, paths->pending, text, sizeof(text), &length);
  if (found != 1) return found;
  unsigned long long started_ms = 0;
  unsigned long long lock_ms = 0;
  if (sscanf(text, "lina-lock-pending 2\nstarted_ms %llu\nlock_ms %llu\n", &started_ms, &lock_ms) != 2) {
    return -1;
  }
  out->started_ms = (uint64_t)started_ms;
  out->lock_ms = (uint64_t)lock_ms;
  return 1;
}

int lina_pending_write(const struct lina_host *host, const struct lina_paths *paths,
                       const struct lina_pending *pending) {
  char text[128];
  int length = snprintf(text, sizeof(text), "lina-lock-pending 2\nstarted_ms %llu\nlock_ms %llu\n",
                        (unsigned long long)pending->started_ms, (unsigned long long)pending->lock_ms);
  if (length <= 0 || (size_t)length >= sizeof(text)) return -1;
  return host->write_file(host->context, paths->pending, text, (size_t)length);
}

int lina_enabled_write(const struct lina_host *host, const struct lina_paths *paths, const char *socket,
                       uint32_t person_uid, uint32_t deadline_ms) {
  char text[160];
  int length = snprintf(text, sizeof(text), "socket=%s\nuid=%u\ndeadline_ms=%u\n", socket,
                        (unsigned)person_uid, (unsigned)deadline_ms);
  if (length <= 0 || (size_t)length >= sizeof(text)) return -1;
  return host->write_file(host->context, paths->enabled, text, (size_t)length);
}

bool lina_enabled_present(const struct lina_host *host, const struct lina_paths *paths) {
  return host->exists(host->context, paths->enabled) == 1;
}

static bool escape(const char *value, char *out, size_t capacity) {
  size_t used = 0;
  for (const unsigned char *cursor = (const unsigned char *)value; *cursor != 0; cursor++) {
    unsigned char byte = *cursor;
    if (byte > 0x20 && byte < 0x7f && byte != '%') {
      if (used + 1 >= capacity) return false;
      out[used++] = (char)byte;
    } else {
      if (used + 3 >= capacity) return false;
      snprintf(out + used, capacity - used, "%%%02X", byte);
      used += 3;
    }
  }
  out[used] = '\0';
  return true;
}

static bool unescape(const char *value, char *out, size_t capacity) {
  size_t used = 0;
  for (const char *cursor = value; *cursor != 0; cursor++) {
    if (used + 1 >= capacity) return false;
    if (*cursor != '%') {
      out[used++] = *cursor;
      continue;
    }
    char hex[3] = {cursor[1], cursor[2]};
    if (hex[0] == '\0' || hex[1] == '\0') return false;
    char *end = NULL;
    long byte = strtol(hex, &end, 16);
    if (end != hex + 2 || byte <= 0 || byte > 0xff) return false;
    out[used++] = (char)byte;
    cursor += 2;
  }
  out[used] = '\0';
  return true;
}

static void seal(const char *escaped_build, const char *rule_text, size_t rule_length, char out[65]) {
  char line[96];
  int length = snprintf(line, sizeof(line), "build %s\n", escaped_build);
  const char *inputs[2] = {line, rule_text};
  size_t lengths[2] = {(size_t)(length > 0 ? length : 0), rule_length};
  lina_sha256_hex_inputs(inputs, lengths, 2, out);
}

int lina_backup_write(const struct lina_host *host, const struct lina_paths *paths,
                      const struct lina_right *rule, const char *build) {
  char rule_text[LINA_RULE_TEXT_MAX];
  size_t rule_length = lina_right_serialize(rule, rule_text, sizeof(rule_text));
  if (rule_length == 0) return -1;
  char escaped_build[64 * 3];
  if (!escape(build, escaped_build, sizeof(escaped_build))) return -1;
  char digest[65];
  seal(escaped_build, rule_text, rule_length, digest);
  char text[LINA_BACKUP_TEXT_MAX];
  int length = snprintf(text, sizeof(text), "lina-lock-backup 1\nbuild %s\nsha256 %s\n%s", escaped_build, digest,
                        rule_text);
  if (length <= 0 || (size_t)length >= sizeof(text)) return -1;
  return host->write_file(host->context, paths->backup, text, (size_t)length);
}

int lina_backup_read(const struct lina_host *host, const struct lina_paths *paths, struct lina_backup *out) {
  static char text[LINA_BACKUP_TEXT_MAX];
  size_t length = 0;
  int found = read_text(host, paths->backup, text, sizeof(text), &length);
  if (found != 1) return found;
  memset(out, 0, sizeof(*out));
  char *cursor = text;
  char *header = strchr(cursor, '\n');
  if (header == NULL || (size_t)(header - text) != 18 || strncmp(text, "lina-lock-backup 1\n", 18) != 0) return -1;
  cursor = header + 1;
  char *build_line = strchr(cursor, '\n');
  if (build_line == NULL || strncmp(cursor, "build ", 6) != 0) return -1;
  *build_line = '\0';
  if (!unescape(cursor + 6, out->build, sizeof(out->build))) return -1;
  cursor = build_line + 1;
  char *hash_line = strchr(cursor, '\n');
  if (hash_line == NULL || strncmp(cursor, "sha256 ", 7) != 0) return -1;
  *hash_line = '\0';
  const char *stored = cursor + 7;
  if (strlen(stored) != 64) return -1;
  char *rule_text = hash_line + 1;
  size_t rule_length = length - (size_t)(rule_text - text);
  char escaped_build[64 * 3];
  if (!escape(out->build, escaped_build, sizeof(escaped_build))) return -1;
  char digest[65];
  seal(escaped_build, rule_text, rule_length, digest);
  if (strcmp(digest, stored) != 0) return -1;
  if (!lina_right_parse(rule_text, rule_length, &out->rule)) return -1;
  return 1;
}
