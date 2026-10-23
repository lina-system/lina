// SPDX-License-Identifier: Apache-2.0
// The rule model: the stock and LINA definitions, equality without timestamps,
// and a text form that round-trips exactly.
#include <string.h>

#include "rule.h"
#include "state.h"
#include "test.h"

static void round_trip(const struct lina_right *right) {
  char text[LINA_RULE_TEXT_MAX];
  size_t length = lina_right_serialize(right, text, sizeof(text));
  CHECK(length > 0);
  struct lina_right parsed;
  CHECK(lina_right_parse(text, length, &parsed));
  CHECK(lina_right_equal(right, &parsed));
  char again[LINA_RULE_TEXT_MAX];
  size_t again_length = lina_right_serialize(&parsed, again, sizeof(again));
  CHECK(again_length == length);
  CHECK_STR(again, text);
}

void test_rule(void) {
  struct lina_right stock;
  struct lina_right lina;
  lina_rule_stock(&stock);
  lina_rule_lina(&lina);
  struct lina_right unlock;
  struct lina_right selftest;
  lina_right_unlock(&unlock);
  lina_right_selftest(&selftest);

  round_trip(&stock);
  round_trip(&lina);
  round_trip(&unlock);

  CHECK(lina_rule_is_stock(&stock));
  CHECK(!lina_rule_is_stock(&lina));
  CHECK(lina_rule_is_lina(&lina));
  CHECK(!lina_rule_is_lina(&stock));
  CHECK(lina_rule_names_lina(&lina));
  CHECK(!lina_rule_names_lina(&stock));
  CHECK_STR(lina_rule_shape(&lina), "lina");
  CHECK_STR(lina_rule_shape(&stock), "other");

  const struct lina_value *class_value = lina_right_get(&unlock, "class");
  CHECK(class_value != NULL && class_value->kind == LINA_VALUE_STRING);
  CHECK_STR(class_value->string, "evaluate-mechanisms");
  const struct lina_value *mechanisms = lina_right_get(&unlock, "mechanisms");
  CHECK(mechanisms != NULL && mechanisms->kind == LINA_VALUE_STRINGS && mechanisms->count == 1);
  CHECK_STR(mechanisms->list[0], "LinaUnlock:unlock,privileged");
  const struct lina_value *tries = lina_right_get(&unlock, "tries");
  CHECK(tries != NULL && tries->kind == LINA_VALUE_INT && tries->integer == 1);
  const struct lina_value *shared = lina_right_get(&unlock, "shared");
  CHECK(shared != NULL && shared->kind == LINA_VALUE_BOOL && !shared->boolean);
  CHECK(lina_right_equal(&unlock, &selftest));

  const struct lina_value *k = lina_right_get(&lina, "k-of-n");
  CHECK(k != NULL && k->kind == LINA_VALUE_INT && k->integer == 1);
  const struct lina_value *rules = lina_right_get(&lina, "rule");
  CHECK(rules != NULL && rules->kind == LINA_VALUE_STRINGS && rules->count == 2);
  CHECK_STR(rules->list[0], LINA_RIGHT_UNLOCK);
  CHECK_STR(rules->list[1], LINA_STOCK_RULE);

  // The stock rule as macOS ships it is a single-rule list of the same name.
  struct lina_right shipped;
  lina_right_clear(&shipped);
  lina_right_set_string(&shipped, "class", "rule");
  lina_right_set_int(&shipped, "version", 1);
  lina_right_set_string(&shipped, "rule", LINA_STOCK_RULE);
  CHECK(lina_rule_is_stock(&shipped));
  round_trip(&shipped);

  // Timestamps are ignored for equality, every other key is not.
  struct lina_right stamped = stock;
  lina_right_set_int(&stamped, "created", 1700000000);
  lina_right_set_int(&stamped, "modified", 1700000001);
  CHECK(!lina_right_equal(&stamped, &stock));
  CHECK(lina_right_equal_ignoring_timestamps(&stamped, &stock));
  struct lina_right changed = stamped;
  lina_right_set_string(&changed, "rule", "authenticate-session-owner-or-admin");
  CHECK(!lina_right_equal_ignoring_timestamps(&changed, &stock));

  // The shapes the install must refuse.
  struct lina_right psso;
  lina_right_clear(&psso);
  lina_right_set_string(&psso, "class", "rule");
  lina_right_set_int(&psso, "k-of-n", 2);
  {
    const char *items[] = {"psso-screensaver", "authenticate-session-owner-or-admin"};
    lina_right_set_list(&psso, "rule", items, 2);
  }
  CHECK_STR(lina_rule_shape(&psso), "platform-sso");
  CHECK(!lina_rule_is_stock(&psso));
  CHECK(!lina_rule_is_lina(&psso));
  round_trip(&psso);

  struct lina_right smart_card;
  lina_right_clear(&smart_card);
  lina_right_set_string(&smart_card, "class", "rule");
  lina_right_set_string(&smart_card, "rule", "CryptoTokenKit:login");
  CHECK_STR(lina_rule_shape(&smart_card), "smart-card");

  struct lina_right other_plugin;
  lina_right_clear(&other_plugin);
  lina_right_set_string(&other_plugin, "class", "rule");
  lina_right_set_string(&other_plugin, "rule", "com.example.vendor:unlock,privileged");
  CHECK_STR(lina_rule_shape(&other_plugin), "plugin");

  // A definition with a list is not the shipped single-rule form even when the
  // only item matches, so the install refuses rather than rewriting it.
  struct lina_right listed_stock;
  lina_right_clear(&listed_stock);
  lina_right_set_string(&listed_stock, "class", "rule");
  {
    const char *items[] = {LINA_STOCK_RULE};
    lina_right_set_list(&listed_stock, "rule", items, 1);
  }
  CHECK(lina_rule_is_stock(&listed_stock));

  // Escaped text survives the round trip.
  struct lina_right escaped;
  lina_right_clear(&escaped);
  lina_right_set_string(&escaped, "note", "a b\nc%20");
  round_trip(&escaped);

  // A damaged text is rejected rather than half parsed.
  char text[LINA_RULE_TEXT_MAX];
  size_t length = lina_right_serialize(&stock, text, sizeof(text));
  struct lina_right parsed;
  CHECK(!lina_right_parse(text, 10, &parsed));
  text[6] = '9';
  CHECK(!lina_right_parse(text, length, &parsed));
}
