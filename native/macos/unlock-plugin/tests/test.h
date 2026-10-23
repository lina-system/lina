// SPDX-License-Identifier: Apache-2.0
// Minimal test harness. Each check reports its file and line; the binary exits
// non-zero when any check failed.
#ifndef LINA_TEST_H
#define LINA_TEST_H

#include <stdio.h>
#include <string.h>

extern int lina_test_checks;
extern int lina_test_failures;

#define CHECK(condition)                                                                      \
  do {                                                                                        \
    lina_test_checks++;                                                                       \
    if (!(condition)) {                                                                       \
      lina_test_failures++;                                                                   \
      fprintf(stderr, "%s:%d: check failed: %s\n", __FILE__, __LINE__, #condition);           \
    }                                                                                         \
  } while (0)

#define CHECK_BYTES(actual, expected, count)                                                   \
  do {                                                                                         \
    lina_test_checks++;                                                                        \
    if (memcmp((actual), (expected), (count)) != 0) {                                          \
      lina_test_failures++;                                                                    \
      fprintf(stderr, "%s:%d: bytes differ from %s\n", __FILE__, __LINE__, #expected);         \
    }                                                                                          \
  } while (0)

void test_decide(void);
void test_deadline(void);
void test_mechanism(void);
void test_plugin(void);

#endif
