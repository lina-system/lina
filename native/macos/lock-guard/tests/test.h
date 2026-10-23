// SPDX-License-Identifier: Apache-2.0
// Minimal test harness. Each check reports its file and line; the binary exits
// non-zero when any check failed.
#ifndef LINA_GUARD_TEST_H
#define LINA_GUARD_TEST_H

#include <stdio.h>
#include <string.h>

extern int lina_test_checks;
extern int lina_test_failures;

#define CHECK(condition)                                                                       \
  do {                                                                                         \
    lina_test_checks++;                                                                        \
    if (!(condition)) {                                                                        \
      lina_test_failures++;                                                                    \
      fprintf(stderr, "%s:%d: check failed: %s\n", __FILE__, __LINE__, #condition);            \
    }                                                                                          \
  } while (0)

#define CHECK_STR(actual, expected)                                                            \
  do {                                                                                         \
    lina_test_checks++;                                                                        \
    if (strcmp((actual), (expected)) != 0) {                                                   \
      lina_test_failures++;                                                                    \
      fprintf(stderr, "%s:%d: expected \"%s\", got \"%s\"\n", __FILE__, __LINE__, (expected),  \
              (actual));                                                                       \
    }                                                                                          \
  } while (0)

#define CHECK_BYTES(actual, expected, count)                                                   \
  do {                                                                                         \
    lina_test_checks++;                                                                        \
    if (memcmp((actual), (expected), (count)) != 0) {                                          \
      lina_test_failures++;                                                                    \
      fprintf(stderr, "%s:%d: bytes differ from %s\n", __FILE__, __LINE__, #expected);          \
    }                                                                                          \
  } while (0)

void test_rule(void);
void test_sha256(void);
void test_state(void);
void test_install(void);
void test_uninstall(void);
void test_guard(void);
void test_restore(void);

#endif
