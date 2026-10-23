// SPDX-License-Identifier: Apache-2.0
#include <stdio.h>

#include "test.h"

int lina_test_checks = 0;
int lina_test_failures = 0;

int main(void) {
  test_rule();
  test_sha256();
  test_state();
  test_install();
  test_uninstall();
  test_guard();
  test_restore();
  printf("lock guard: %d checks, %d failures\n", lina_test_checks, lina_test_failures);
  return lina_test_failures == 0 ? 0 : 1;
}
