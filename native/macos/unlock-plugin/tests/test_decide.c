// SPDX-License-Identifier: Apache-2.0
// Every deny path and the one allow path of the decision core.
#include <string.h>

#include "fake_host.h"
#include "lina_unlock.h"
#include "test.h"

static void run(struct fake_host *fake, struct lina_result *result, void (*setup)(struct fake_host *)) {
  fake_host_init(fake);
  if (setup != NULL) setup(fake);
  *result = lina_decide(&fake->host);
}

static void not_enabled(struct fake_host *fake) { fake->config.enabled = false; }
static void config_unreadable(struct fake_host *fake) { fake->load_config_status = -1; }
static void stale_marker(struct fake_host *fake) { fake->marker_present = 1; }
static void marker_unreadable(struct fake_host *fake) { fake->marker_present = -1; }
static void marker_unwritable(struct fake_host *fake) { fake->write_marker_status = -1; }
static void no_socket(struct fake_host *fake) { fake->connect_status = -1; }
static void slow_connect(struct fake_host *fake) {
  fake->connect_status = -1;
  fake->connect_timed_out = true;
}
static void no_peer_token(struct fake_host *fake) { fake->token_status = -1; }
static void peer_uninspectable(struct fake_host *fake) { fake->inspect_status = -1; }
static void bad_signature(struct fake_host *fake) { fake->report.signature_ok = false; }
static void no_hardened_runtime(struct fake_host *fake) { fake->report.hardened_runtime = false; }
static void debug_entitlement(struct fake_host *fake) { fake->report.get_task_allow = true; }
static void wrong_user(struct fake_host *fake) { fake->report.same_user = false; }
static void no_nonce(struct fake_host *fake) { fake->random_status = -1; }
static void write_failed(struct fake_host *fake) { fake->write_status = -1; }
static void write_timed_out(struct fake_host *fake) {
  fake->write_status = -1;
  fake->write_timed_out = true;
}
static void read_failed(struct fake_host *fake) { fake->read_status = -1; }
static void no_reply(struct fake_host *fake) {
  fake->read_status = -1;
  fake->read_timed_out = true;
  fake->config.deadline_ms = 200;
}
static void bad_magic(struct fake_host *fake) { fake->reply_bad_magic = true; }
static void wrong_nonce(struct fake_host *fake) { fake->reply_nonce_mismatch = true; }
static void not_armed(struct fake_host *fake) { fake->reply_armed = false; }
static void zero_ticket(struct fake_host *fake) { memset(fake->ticket, 0, LINA_TICKET_BYTES); }
static void changed_peer(struct fake_host *fake) { fake->second_token.bytes[0] ^= 0xffu; }
static void second_token_failed(struct fake_host *fake) { fake->second_token_status = -1; }
static void short_deadline(struct fake_host *fake) { fake->config.deadline_ms = 0; }
static void long_deadline(struct fake_host *fake) { fake->config.deadline_ms = 5000; }

static void expect_deny(enum lina_decision expected, void (*setup)(struct fake_host *), const char *name) {
  struct fake_host fake;
  struct lina_result result;
  run(&fake, &result, setup);
  if (result.decision != expected) {
    fprintf(stderr, "  %s: expected %s, got %s\n", name, lina_decision_name(expected),
            lina_decision_name(result.decision));
  }
  CHECK(result.decision == expected);
  CHECK(!lina_decision_allows(result.decision));
}

static void test_allow_path(void) {
  struct fake_host fake;
  struct lina_result result;
  run(&fake, &result, NULL);
  CHECK(result.decision == LINA_ALLOW);
  CHECK(lina_decision_allows(result.decision));
  CHECK_BYTES(result.ticket, fake.ticket, LINA_TICKET_BYTES);
  CHECK(result.deadline_ms == fake.config.deadline_ms);
  CHECK(fake.marker_written);
  CHECK(fake.marker_cleared);
  CHECK(fake.connect_calls == 1);
  CHECK(fake.close_calls == 1);
  CHECK(fake.random_calls == 1);
  CHECK(fake.read_calls == 1);
  CHECK(fake.second_token_read);
}

static void test_marker_lifecycle(void) {
  struct fake_host fake;
  struct lina_result result;
  run(&fake, &result, no_socket);
  CHECK(fake.marker_written);
  CHECK(fake.marker_cleared);
  CHECK(fake.close_calls == 0);

  run(&fake, &result, stale_marker);
  CHECK(!fake.marker_written);
  CHECK(!fake.marker_cleared);
  CHECK(fake.connect_calls == 0);

  run(&fake, &result, not_enabled);
  CHECK(!fake.marker_written);
  CHECK(fake.connect_calls == 0);
}

static void test_deadline_is_bounded(void) {
  struct fake_host fake;
  struct lina_result result;
  run(&fake, &result, long_deadline);
  CHECK(fake.connect_timeout <= LINA_MAX_DEADLINE_MS);
  CHECK(result.deadline_ms == LINA_MAX_DEADLINE_MS);
  CHECK(fake.write_timeout <= LINA_MAX_DEADLINE_MS);
  CHECK(fake.read_timeout <= LINA_MAX_DEADLINE_MS);

  run(&fake, &result, short_deadline);
  CHECK(result.deadline_ms >= 1);
  CHECK(fake.connect_timeout >= 1);
}

static void test_decision_values(void) {
  for (int value = 0; value < (int)LINA_DECISION_COUNT; value++) {
    enum lina_decision decision = (enum lina_decision)value;
    CHECK(lina_decision_name(decision) != NULL);
    CHECK(strcmp(lina_decision_name(decision), "stub") != 0);
    if (decision == LINA_ALLOW) CHECK(lina_decision_allows(decision));
    else CHECK(!lina_decision_allows(decision));
  }
}

void test_decide(void) {
  test_allow_path();
  test_marker_lifecycle();
  test_deadline_is_bounded();
  test_decision_values();

  expect_deny(LINA_DENY_NOT_ENABLED, not_enabled, "no enabled marker");
  expect_deny(LINA_DENY_INTERNAL, config_unreadable, "unreadable install state");
  expect_deny(LINA_DENY_CRASH_MARKER, stale_marker, "stale crash marker");
  expect_deny(LINA_DENY_INTERNAL, marker_unreadable, "unreadable crash marker");
  expect_deny(LINA_DENY_INTERNAL, marker_unwritable, "unwritable crash marker");
  expect_deny(LINA_DENY_NO_SOCKET, no_socket, "no socket");
  expect_deny(LINA_DENY_CONNECT_TIMEOUT, slow_connect, "slow connect");
  expect_deny(LINA_DENY_PEER_TOKEN, no_peer_token, "no peer token");
  expect_deny(LINA_DENY_INTERNAL, peer_uninspectable, "uninspectable peer");
  expect_deny(LINA_DENY_SIGNATURE, bad_signature, "signature mismatch");
  expect_deny(LINA_DENY_HARDENED_RUNTIME, no_hardened_runtime, "no hardened runtime");
  expect_deny(LINA_DENY_DEBUG_ENTITLEMENT, debug_entitlement, "debugging entitlement");
  expect_deny(LINA_DENY_WRONG_USER, wrong_user, "wrong user");
  expect_deny(LINA_DENY_NO_NONCE, no_nonce, "no nonce");
  expect_deny(LINA_DENY_IO, write_failed, "socket write failed");
  expect_deny(LINA_DENY_DEADLINE, write_timed_out, "socket write timed out");
  expect_deny(LINA_DENY_IO, read_failed, "socket read failed");
  expect_deny(LINA_DENY_DEADLINE, no_reply, "no reply");
  expect_deny(LINA_DENY_INTERNAL, bad_magic, "bad reply magic");
  expect_deny(LINA_DENY_WRONG_NONCE, wrong_nonce, "wrong nonce");
  expect_deny(LINA_DENY_NO_TICKET, not_armed, "unarmed ticket");
  expect_deny(LINA_DENY_NO_TICKET, zero_ticket, "zero ticket");
  expect_deny(LINA_DENY_PEER_CHANGED, changed_peer, "changed audit token");
  expect_deny(LINA_DENY_PEER_TOKEN, second_token_failed, "second token failed");
}
