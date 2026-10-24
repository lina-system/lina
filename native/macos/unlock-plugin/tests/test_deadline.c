// SPDX-License-Identifier: Apache-2.0
// The deadline holds whatever the socket does: a peer that never answers, a
// connect that never completes, and a clock that has already passed it.
#include "fake_host.h"
#include "lina_unlock.h"
#include "test.h"

static void never_answers(struct fake_host *fake) {
  fake->read_status = -1;
  fake->read_timed_out = true;
  fake->config.deadline_ms = 200;
}

static void slow_connect(struct fake_host *fake) {
  fake->connect_status = -1;
  fake->connect_timed_out = true;
  fake->config.deadline_ms = 200;
}

static void clock_already_past(struct fake_host *fake) {
  fake->tick_ms = 1000;
  fake->config.deadline_ms = 200;
}

static void test_silent_peer(void) {
  struct fake_host fake;
  struct lina_result result;
  fake_host_init(&fake);
  never_answers(&fake);
  result = lina_decide(&fake.host);
  CHECK(result.decision == LINA_DENY_DEADLINE);
  CHECK(!lina_decision_allows(result.decision));
  // The host used exactly the deadline, and no more, before the answer.
  CHECK(fake.now == fake.config.deadline_ms);
  CHECK(fake.read_timeout <= fake.config.deadline_ms);
  CHECK(fake.write_timeout <= fake.config.deadline_ms);
  CHECK(fake.marker_cleared);
}

static void test_slow_connect(void) {
  struct fake_host fake;
  struct lina_result result;
  fake_host_init(&fake);
  slow_connect(&fake);
  result = lina_decide(&fake.host);
  CHECK(result.decision == LINA_DENY_CONNECT_TIMEOUT);
  CHECK(fake.connect_timeout <= fake.config.deadline_ms);
  CHECK(fake.now == fake.config.deadline_ms);
  CHECK(fake.read_calls == 0);
  CHECK(fake.marker_cleared);
}

static void test_past_deadline(void) {
  struct fake_host fake;
  struct lina_result result;
  fake_host_init(&fake);
  clock_already_past(&fake);
  result = lina_decide(&fake.host);
  CHECK(result.decision == LINA_DENY_DEADLINE);
  CHECK(fake.connect_calls == 0);
  CHECK(fake.marker_written);
  CHECK(fake.marker_cleared);
}

void test_deadline(void) {
  test_silent_peer();
  test_slow_connect();
  test_past_deadline();
}
