// SPDX-License-Identifier: Apache-2.0
#include "rule.h"

#include <stdarg.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

static void copy_string(char *out, size_t capacity, const char *value) {
  snprintf(out, capacity, "%s", value);
}

void lina_right_clear(struct lina_right *right) { memset(right, 0, sizeof(*right)); }

const struct lina_value *lina_right_get(const struct lina_right *right, const char *key) {
  for (uint8_t i = 0; i < right->count; i++) {
    if (strcmp(right->entries[i].key, key) == 0) return &right->entries[i].value;
  }
  return NULL;
}

bool lina_right_set_value(struct lina_right *right, const char *key, const struct lina_value *value) {
  if (strlen(key) >= LINA_RULE_KEY_MAX) return false;
  for (uint8_t i = 0; i < right->count; i++) {
    if (strcmp(right->entries[i].key, key) == 0) {
      right->entries[i].value = *value;
      return true;
    }
  }
  if (right->count == LINA_RULE_MAX_ENTRIES) return false;
  copy_string(right->entries[right->count].key, LINA_RULE_KEY_MAX, key);
  right->entries[right->count].value = *value;
  right->count++;
  return true;
}

static struct lina_value *mutable_value(struct lina_right *right, const char *key) {
  for (uint8_t i = 0; i < right->count; i++) {
    if (strcmp(right->entries[i].key, key) == 0) return &right->entries[i].value;
  }
  return NULL;
}

static bool prepare(struct lina_right *right, const char *key, enum lina_value_kind kind) {
  if (strlen(key) >= LINA_RULE_KEY_MAX) return false;
  struct lina_value *value = mutable_value(right, key);
  if (value != NULL) {
    memset(value, 0, sizeof(*value));
    value->kind = kind;
    return true;
  }
  if (right->count == LINA_RULE_MAX_ENTRIES) return false;
  copy_string(right->entries[right->count].key, LINA_RULE_KEY_MAX, key);
  value = &right->entries[right->count].value;
  memset(value, 0, sizeof(*value));
  value->kind = kind;
  right->count++;
  return true;
}

bool lina_right_set_string(struct lina_right *right, const char *key, const char *value) {
  if (strlen(value) >= LINA_RULE_STRING_MAX) return false;
  if (!prepare(right, key, LINA_VALUE_STRING)) return false;
  copy_string(mutable_value(right, key)->string, LINA_RULE_STRING_MAX, value);
  return true;
}

bool lina_right_set_bool(struct lina_right *right, const char *key, bool value) {
  if (!prepare(right, key, LINA_VALUE_BOOL)) return false;
  mutable_value(right, key)->boolean = value;
  return true;
}

bool lina_right_set_int(struct lina_right *right, const char *key, long long value) {
  if (!prepare(right, key, LINA_VALUE_INT)) return false;
  mutable_value(right, key)->integer = value;
  return true;
}

bool lina_right_set_list(struct lina_right *right, const char *key, const char *const *items, size_t count) {
  if (count > LINA_RULE_LIST_MAX) return false;
  for (size_t i = 0; i < count; i++) {
    if (strlen(items[i]) >= LINA_RULE_STRING_MAX) return false;
  }
  if (!prepare(right, key, LINA_VALUE_STRINGS)) return false;
  struct lina_value *value = mutable_value(right, key);
  value->count = (uint8_t)count;
  for (size_t i = 0; i < count; i++) copy_string(value->list[i], LINA_RULE_STRING_MAX, items[i]);
  return true;
}

bool lina_right_remove(struct lina_right *right, const char *key) {
  for (uint8_t i = 0; i < right->count; i++) {
    if (strcmp(right->entries[i].key, key) != 0) continue;
    for (uint8_t j = i; j + 1 < right->count; j++) right->entries[j] = right->entries[j + 1];
    right->count--;
    return true;
  }
  return false;
}

static bool is_timestamp_key(const char *key) {
  return strcmp(key, "created") == 0 || strcmp(key, "modified") == 0 || strcmp(key, "timestamp") == 0;
}

static bool value_equal(const struct lina_value *left, const struct lina_value *right) {
  if (left->kind != right->kind) return false;
  switch (left->kind) {
    case LINA_VALUE_STRING:
      return strcmp(left->string, right->string) == 0;
    case LINA_VALUE_BOOL:
      return left->boolean == right->boolean;
    case LINA_VALUE_INT:
      return left->integer == right->integer;
    case LINA_VALUE_STRINGS:
      if (left->count != right->count) return false;
      for (uint8_t i = 0; i < left->count; i++) {
        if (strcmp(left->list[i], right->list[i]) != 0) return false;
      }
      return true;
  }
  return false;
}

static bool equal_with(const struct lina_right *left, const struct lina_right *right, bool ignore_timestamps) {
  uint8_t left_count = 0;
  uint8_t right_count = 0;
  for (uint8_t i = 0; i < left->count; i++) {
    if (!ignore_timestamps || !is_timestamp_key(left->entries[i].key)) left_count++;
  }
  for (uint8_t i = 0; i < right->count; i++) {
    if (!ignore_timestamps || !is_timestamp_key(right->entries[i].key)) right_count++;
  }
  if (left_count != right_count) return false;
  for (uint8_t i = 0; i < left->count; i++) {
    if (ignore_timestamps && is_timestamp_key(left->entries[i].key)) continue;
    const struct lina_value *other = lina_right_get(right, left->entries[i].key);
    if (other == NULL || !value_equal(&left->entries[i].value, other)) return false;
  }
  return true;
}

bool lina_right_equal(const struct lina_right *left, const struct lina_right *right) {
  return equal_with(left, right, false);
}

bool lina_right_equal_ignoring_timestamps(const struct lina_right *left, const struct lina_right *right) {
  return equal_with(left, right, true);
}

static bool append(char *out, size_t capacity, size_t *used, const char *format, ...) {
  va_list arguments;
  va_start(arguments, format);
  int written = vsnprintf(out + *used, capacity - *used, format, arguments);
  va_end(arguments);
  if (written < 0 || (size_t)written >= capacity - *used) return false;
  *used += (size_t)written;
  return true;
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

static bool append_value(struct lina_value *value, char *out, size_t capacity, size_t *used, const char *key) {
  switch (value->kind) {
    case LINA_VALUE_STRING: {
      char escaped[LINA_RULE_STRING_MAX * 3];
      if (!escape(value->string, escaped, sizeof(escaped))) return false;
      return append(out, capacity, used, "%s s %s\n", key, escaped);
    }
    case LINA_VALUE_BOOL:
      return append(out, capacity, used, "%s b %d\n", key, value->boolean ? 1 : 0);
    case LINA_VALUE_INT:
      return append(out, capacity, used, "%s i %lld\n", key, value->integer);
    case LINA_VALUE_STRINGS: {
      if (!append(out, capacity, used, "%s a %u", key, (unsigned)value->count)) return false;
      for (uint8_t i = 0; i < value->count; i++) {
        char escaped[LINA_RULE_STRING_MAX * 3];
        if (!escape(value->list[i], escaped, sizeof(escaped))) return false;
        if (!append(out, capacity, used, " %s", escaped)) return false;
      }
      return append(out, capacity, used, "\n");
    }
  }
  return false;
}

size_t lina_right_serialize(const struct lina_right *right, char *out, size_t capacity) {
  size_t used = 0;
  if (capacity == 0) return 0;
  out[0] = '\0';
  if (!append(out, capacity, &used, "right %u\n", (unsigned)right->count)) return 0;
  uint8_t order[LINA_RULE_MAX_ENTRIES];
  for (uint8_t i = 0; i < right->count; i++) order[i] = i;
  for (uint8_t i = 1; i < right->count; i++) {
    uint8_t current = order[i];
    uint8_t j = i;
    while (j > 0 && strcmp(right->entries[order[j - 1]].key, right->entries[current].key) > 0) {
      order[j] = order[j - 1];
      j--;
    }
    order[j] = current;
  }
  for (uint8_t i = 0; i < right->count; i++) {
    const struct lina_entry *entry = &right->entries[order[i]];
    struct lina_value value = entry->value;
    if (!append_value(&value, out, capacity, &used, entry->key)) return 0;
  }
  return used;
}

static bool split_line(char *line, char **key, char **type, char **rest) {
  char *first = strchr(line, ' ');
  if (first == NULL) return false;
  *first = '\0';
  *key = line;
  *type = first + 1;
  char *second = strchr(*type, ' ');
  if (second == NULL) {
    *rest = *type + strlen(*type);
    return (*type)[0] != '\0';
  }
  *second = '\0';
  *rest = second + 1;
  return (*type)[0] != '\0';
}

static bool parse_value(char *key, char *type, char *rest, struct lina_right *right) {
  struct lina_value value;
  memset(&value, 0, sizeof(value));
  if (strcmp(type, "s") == 0) {
    value.kind = LINA_VALUE_STRING;
    if (!unescape(rest, value.string, sizeof(value.string))) return false;
  } else if (strcmp(type, "b") == 0) {
    if (strcmp(rest, "0") != 0 && strcmp(rest, "1") != 0) return false;
    value.kind = LINA_VALUE_BOOL;
    value.boolean = rest[0] == '1';
  } else if (strcmp(type, "i") == 0) {
    char *end = NULL;
    long long parsed = strtoll(rest, &end, 10);
    if (end == rest || *end != '\0') return false;
    value.kind = LINA_VALUE_INT;
    value.integer = parsed;
  } else if (strcmp(type, "a") == 0) {
    char *end = NULL;
    long count = strtol(rest, &end, 10);
    if (end == rest || count < 0 || count > LINA_RULE_LIST_MAX) return false;
    value.kind = LINA_VALUE_STRINGS;
    value.count = (uint8_t)count;
    char *cursor = end;
    for (uint8_t i = 0; i < value.count; i++) {
      while (*cursor == ' ') cursor++;
      if (*cursor == '\0') return false;
      char *item = cursor;
      char *space = strchr(item, ' ');
      if (space != NULL) {
        *space = '\0';
        cursor = space + 1;
      } else {
        cursor = item + strlen(item);
      }
      if (!unescape(item, value.list[i], sizeof(value.list[i]))) return false;
    }
    if (*cursor != '\0') return false;
  } else {
    return false;
  }
  return lina_right_set_value(right, key, &value);
}

bool lina_right_parse(const char *text, size_t length, struct lina_right *out) {
  if (text == NULL || length == 0 || length >= LINA_RULE_TEXT_MAX) return false;
  char buffer[LINA_RULE_TEXT_MAX];
  memcpy(buffer, text, length);
  buffer[length] = '\0';
  lina_right_clear(out);
  char *cursor = buffer;
  char *newline = strchr(cursor, '\n');
  if (newline == NULL) return false;
  *newline = '\0';
  if (strncmp(cursor, "right ", 6) != 0) return false;
  char *end = NULL;
  long declared = strtol(cursor + 6, &end, 10);
  if (end == cursor + 6 || *end != '\0' || declared < 0 || declared > LINA_RULE_MAX_ENTRIES) return false;
  cursor = newline + 1;
  long parsed = 0;
  while (*cursor != '\0') {
    newline = strchr(cursor, '\n');
    if (newline != NULL) *newline = '\0';
    if (*cursor == '\0') {
      cursor = newline == NULL ? cursor : newline + 1;
      continue;
    }
    char *key;
    char *type;
    char *rest;
    if (!split_line(cursor, &key, &type, &rest)) return false;
    if (key[0] == '\0' || !parse_value(key, type, rest, out)) return false;
    parsed++;
    if (newline == NULL) break;
    cursor = newline + 1;
  }
  return parsed == declared && out->count == declared;
}

static const struct lina_value *rule_list(const struct lina_right *right) {
  const struct lina_value *value = lina_right_get(right, "rule");
  if (value == NULL) return NULL;
  if (value->kind != LINA_VALUE_STRING && value->kind != LINA_VALUE_STRINGS) return NULL;
  return value;
}

static size_t list_count(const struct lina_value *value) {
  return value->kind == LINA_VALUE_STRING ? 1 : value->count;
}

static const char *list_at(const struct lina_value *value, size_t index) {
  return value->kind == LINA_VALUE_STRING ? (index == 0 ? value->string : NULL) : value->list[index];
}

static bool class_is(const struct lina_right *right, const char *name) {
  const struct lina_value *value = lina_right_get(right, "class");
  return value != NULL && value->kind == LINA_VALUE_STRING && strcmp(value->string, name) == 0;
}

static bool k_of_n_is_one(const struct lina_right *right) {
  const struct lina_value *value = lina_right_get(right, "k-of-n");
  if (value == NULL) return false;
  return value->kind == LINA_VALUE_INT && value->integer == 1;
}

void lina_rule_stock(struct lina_right *right) {
  lina_right_clear(right);
  lina_right_set_string(right, "class", "rule");
  lina_right_set_string(right, "rule", LINA_STOCK_RULE);
  lina_right_set_int(right, "version", 1);
}

void lina_rule_lina(struct lina_right *right) {
  const char *items[] = {LINA_RIGHT_UNLOCK, LINA_STOCK_RULE};
  lina_right_clear(right);
  lina_right_set_string(right, "class", "rule");
  lina_right_set_int(right, "k-of-n", 1);
  lina_right_set_list(right, "rule", items, 2);
  lina_right_set_int(right, "version", 1);
}

void lina_right_unlock(struct lina_right *right) {
  const char *mechanisms[] = {LINA_UNLOCK_MECHANISM};
  lina_right_clear(right);
  lina_right_set_string(right, "class", "evaluate-mechanisms");
  lina_right_set_list(right, "mechanisms", mechanisms, 1);
  lina_right_set_int(right, "tries", 1);
  lina_right_set_bool(right, "shared", false);
}

void lina_right_selftest(struct lina_right *right) { lina_right_unlock(right); }

bool lina_rule_is_stock(const struct lina_right *right) {
  if (!class_is(right, "rule")) return false;
  if (lina_right_get(right, "mechanisms") != NULL) return false;
  const struct lina_value *k = lina_right_get(right, "k-of-n");
  if (k != NULL && (k->kind != LINA_VALUE_INT || k->integer != 1)) return false;
  const struct lina_value *rules = rule_list(right);
  if (rules == NULL || list_count(rules) != 1) return false;
  return strcmp(list_at(rules, 0), LINA_STOCK_RULE) == 0;
}

bool lina_rule_is_lina(const struct lina_right *right) {
  if (!class_is(right, "rule")) return false;
  if (!k_of_n_is_one(right)) return false;
  const struct lina_value *rules = rule_list(right);
  if (rules == NULL || list_count(rules) != 2) return false;
  return strcmp(list_at(rules, 0), LINA_RIGHT_UNLOCK) == 0 && strcmp(list_at(rules, 1), LINA_STOCK_RULE) == 0;
}

static bool mentions_lina(const struct lina_value *value) {
  if (value == NULL) return false;
  if (value->kind == LINA_VALUE_STRING) {
    return strcmp(value->string, LINA_RIGHT_UNLOCK) == 0 || strcmp(value->string, LINA_UNLOCK_MECHANISM) == 0;
  }
  if (value->kind == LINA_VALUE_STRINGS) {
    for (uint8_t i = 0; i < value->count; i++) {
      if (strcmp(value->list[i], LINA_RIGHT_UNLOCK) == 0 || strcmp(value->list[i], LINA_UNLOCK_MECHANISM) == 0) {
        return true;
      }
    }
  }
  return false;
}

bool lina_rule_names_lina(const struct lina_right *right) {
  return mentions_lina(rule_list(right)) || mentions_lina(lina_right_get(right, "mechanisms"))
      || mentions_lina(lina_right_get(right, "right"));
}

static bool contains_text(const char *haystack, const char *needle) {
  size_t length = strlen(needle);
  for (const char *cursor = haystack; *cursor != '\0'; cursor++) {
    size_t i = 0;
    while (i < length) {
      char left = cursor[i];
      if (left >= 'A' && left <= 'Z') left = (char)(left - 'A' + 'a');
      char right = needle[i];
      if (right >= 'A' && right <= 'Z') right = (char)(right - 'A' + 'a');
      if (left != right) break;
      i++;
    }
    if (i == length) return true;
  }
  return false;
}

static bool any_mentions(const struct lina_right *right, const char *needle) {
  for (uint8_t i = 0; i < right->count; i++) {
    const struct lina_value *value = &right->entries[i].value;
    if (value->kind == LINA_VALUE_STRING) {
      if (contains_text(value->string, needle)) return true;
    } else if (value->kind == LINA_VALUE_STRINGS) {
      for (uint8_t j = 0; j < value->count; j++) {
        if (contains_text(value->list[j], needle)) return true;
      }
    }
  }
  return false;
}

static bool any_mentions_a_mechanism(const struct lina_right *right) {
  for (uint8_t i = 0; i < right->count; i++) {
    const struct lina_value *value = &right->entries[i].value;
    if (value->kind == LINA_VALUE_STRING && strchr(value->string, ':') != NULL) return true;
    if (value->kind == LINA_VALUE_STRINGS) {
      for (uint8_t j = 0; j < value->count; j++) {
        if (strchr(value->list[j], ':') != NULL) return true;
      }
    }
  }
  return false;
}

const char *lina_rule_shape(const struct lina_right *right) {
  if (lina_rule_names_lina(right)) return "lina";
  if (any_mentions(right, "psso") || any_mentions(right, "platform")) return "platform-sso";
  if (any_mentions(right, "smartcard") || any_mentions(right, "smart-card")
      || any_mentions(right, "cryptotokenkit")) {
    return "smart-card";
  }
  if (any_mentions_a_mechanism(right)) return "plugin";
  return "other";
}
