// SPDX-License-Identifier: Apache-2.0
// The small, typed view of an authorization right definition that the lock
// guard and the installer need. It models the keys macOS stores for a lock
// screen rule: strings, booleans, integers and string lists. A definition with
// any other value type is reported as unmodelled, so the installer refuses it
// rather than writing back something it cannot reproduce.
#ifndef LINA_RULE_H
#define LINA_RULE_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#define LINA_RULE_MAX_ENTRIES 12
#define LINA_RULE_KEY_MAX 32
#define LINA_RULE_STRING_MAX 128
#define LINA_RULE_LIST_MAX 4
#define LINA_RULE_TEXT_MAX 2048

#define LINA_RIGHT_SCREENSAVER "system.login.screensaver"
#define LINA_RIGHT_UNLOCK "com.lina.locked-screen.unlock"
#define LINA_RIGHT_SELFTEST "com.lina.locked-screen.selftest"
#define LINA_STOCK_RULE "use-login-window-ui"
#define LINA_UNLOCK_MECHANISM "LinaUnlock:unlock,privileged"

enum lina_value_kind {
  LINA_VALUE_STRING = 1,
  LINA_VALUE_BOOL,
  LINA_VALUE_INT,
  LINA_VALUE_STRINGS,
};

struct lina_value {
  enum lina_value_kind kind;
  char string[LINA_RULE_STRING_MAX];
  bool boolean;
  long long integer;
  uint8_t count;
  char list[LINA_RULE_LIST_MAX][LINA_RULE_STRING_MAX];
};

struct lina_entry {
  char key[LINA_RULE_KEY_MAX];
  struct lina_value value;
};

struct lina_right {
  uint8_t count;
  struct lina_entry entries[LINA_RULE_MAX_ENTRIES];
};

void lina_right_clear(struct lina_right *right);
const struct lina_value *lina_right_get(const struct lina_right *right, const char *key);
bool lina_right_set_string(struct lina_right *right, const char *key, const char *value);
bool lina_right_set_bool(struct lina_right *right, const char *key, bool value);
bool lina_right_set_int(struct lina_right *right, const char *key, long long value);
bool lina_right_set_list(struct lina_right *right, const char *key, const char *const *items, size_t count);
bool lina_right_set_value(struct lina_right *right, const char *key, const struct lina_value *value);
bool lina_right_remove(struct lina_right *right, const char *key);

// Equality ignores the timestamps an authorization database keeps for its own
// bookkeeping; every other key and value must match.
bool lina_right_equal(const struct lina_right *left, const struct lina_right *right);
bool lina_right_equal_ignoring_timestamps(const struct lina_right *left, const struct lina_right *right);

// A canonical, sorted, percent-escaped text form. It is what the backup file
// holds and what the hash covers.
size_t lina_right_serialize(const struct lina_right *right, char *out, size_t capacity);
bool lina_right_parse(const char *text, size_t length, struct lina_right *out);

// The rule `system.login.screensaver` ships with, the rule LINA writes, and
// the two rights that name the plug-in.
void lina_rule_stock(struct lina_right *right);
void lina_rule_lina(struct lina_right *right);
void lina_right_unlock(struct lina_right *right);
void lina_right_selftest(struct lina_right *right);

bool lina_rule_is_stock(const struct lina_right *right);
bool lina_rule_is_lina(const struct lina_right *right);
// True when any rule entry or mechanism of this definition names LINA's plug-in.
bool lina_rule_names_lina(const struct lina_right *right);
// A short label for why an unknown rule is not ours: `platform-sso`,
// `smart-card`, `plugin`, `other`.
const char *lina_rule_shape(const struct lina_right *right);

#endif
