// SPDX-License-Identifier: Apache-2.0
#include <stdio.h>

#include "test.h"

int lina_test_checks = 0;
int lina_test_failures = 0;

int main(void) {
  test_decide();
  test_deadline();
  test_mechanism();
  test_plugin();
  printf("native decision core: %d checks, %d failures\n", lina_test_checks, lina_test_failures);
  return lina_test_failures == 0 ? 0 : 1;
}
