// SPDX-License-Identifier: Apache-2.0
#include <stdio.h>
#include <string.h>

#include "fake_host.h"
#include "protocol.h"

static int fake_load_config(void *context, struct lina_config *out) {
  struct fake_host *fake = context;
  if (fake->load_config_status != 0) return fake->load_config_status;
  *out = fake->config;
  return 0;
}

static int fake_crash_marker_present(void *context) {
  struct fake_host *fake = context;
  return fake->marker_present;
}

static int fake_write_crash_marker(void *context) {
  struct fake_host *fake = context;
  if (fake->write_marker_status != 0) return fake->write_marker_status;
  fake->marker_written = true;
  return 0;
}

static void fake_clear_crash_marker(void *context) {
  struct fake_host *fake = context;
  fake->marker_cleared = true;
}

static int fake_connect_broker(void *context, const char *path, uint32_t timeout_ms, bool *timed_out) {
  struct fake_host *fake = context;
  (void)path;
  fake->connect_calls++;
  fake->connect_timeout = timeout_ms;
  if (fake->connect_status != 0) {
    if (fake->connect_timed_out) {
      *timed_out = true;
      fake->now += timeout_ms;
    }
    return -1;
  }
  return 42;
}

static int fake_peer_token(void *context, int fd, struct lina_token *out) {
  struct fake_host *fake = context;
  (void)fd;
  if (!fake->second_token_read) {
    if (fake->token_status != 0) return fake->token_status;
    *out = fake->token;
    fake->second_token_read = true;
    return 0;
  }
  if (fake->second_token_status != 0) return fake->second_token_status;
  *out = fake->second_token;
  return 0;
}

static int fake_inspect_peer(void *context, const struct lina_token *token, uint32_t person_uid,
                             struct lina_peer_report *out) {
  struct fake_host *fake = context;
  (void)token;
  (void)person_uid;
  if (fake->inspect_status != 0) return fake->inspect_status;
  *out = fake->report;
  return 0;
}

static uint64_t fake_monotonic_ms(void *context) {
  struct fake_host *fake = context;
  uint64_t value = fake->now;
  fake->now += fake->tick_ms;
  return value;
}

static int fake_random_bytes(void *context, uint8_t *out, size_t len) {
  struct fake_host *fake = context;
  fake->random_calls++;
  if (fake->random_status != 0) return fake->random_status;
  for (size_t index = 0; index < len; index++) fake->nonce[index] = (uint8_t)(0xa0u + index);
  memcpy(out, fake->nonce, len);
  return 0;
}

static int fake_write_all(void *context, int fd, const void *buf, size_t len, uint32_t timeout_ms, bool *timed_out) {
  struct fake_host *fake = context;
  (void)fd;
  (void)buf;
  fake->write_timeout = timeout_ms;
  if (fake->write_status != 0) {
    if (fake->write_timed_out) {
      *timed_out = true;
      fake->now += timeout_ms;
    }
    return -1;
  }
  return len == LINA_REQUEST_BYTES ? 0 : -1;
}

static int fake_read_all(void *context, int fd, void *buf, size_t len, uint32_t timeout_ms, bool *timed_out) {
  struct fake_host *fake = context;
  (void)fd;
  fake->read_calls++;
  fake->read_timeout = timeout_ms;
  if (fake->read_status != 0) {
    if (fake->read_timed_out) {
      *timed_out = true;
      fake->now += timeout_ms;
    }
    return -1;
  }
  if (len != LINA_REPLY_BYTES) return -1;
  uint8_t *frame = buf;
  memcpy(frame, fake->reply_bad_magic ? "BAD!" : LINA_REPLY_MAGIC, 4);
  frame[4] = LINA_PROTOCOL_VERSION;
  frame[5] = fake->reply_armed ? LINA_REPLY_ARMED : 0u;
  frame[6] = 0;
  frame[7] = 0;
  memcpy(frame + LINA_REPLY_NONCE_OFFSET, fake->nonce, LINA_NONCE_BYTES);
  if (fake->reply_nonce_mismatch) frame[LINA_REPLY_NONCE_OFFSET] ^= 0xffu;
  memcpy(frame + LINA_REPLY_TICKET_OFFSET, fake->ticket, LINA_TICKET_BYTES);
  return 0;
}

static void fake_close_fd(void *context, int fd) {
  struct fake_host *fake = context;
  (void)fd;
  fake->close_calls++;
}

void fake_host_init(struct fake_host *fake) {
  memset(fake, 0, sizeof *fake);
  fake->config.enabled = true;
  snprintf(fake->config.socket_path, sizeof fake->config.socket_path, "/tmp/lina-lock-broker.sock");
  fake->config.person_uid = 501;
  fake->config.deadline_ms = 200;

  fake->report.signature_ok = true;
  fake->report.hardened_runtime = true;
  fake->report.get_task_allow = false;
  fake->report.same_user = true;

  for (size_t index = 0; index < LINA_TOKEN_BYTES; index++) {
    fake->token.bytes[index] = (uint8_t)(index + 1u);
    fake->second_token.bytes[index] = (uint8_t)(index + 1u);
  }
  fake->token.auid = 501;
  fake->token.euid = 501;
  fake->token.pid = 900;
  fake->second_token = fake->token;

  for (size_t index = 0; index < LINA_TICKET_BYTES; index++) fake->ticket[index] = (uint8_t)(0x10u + index);
  fake->reply_armed = true;

  fake->host.context = fake;
  fake->host.load_config = fake_load_config;
  fake->host.crash_marker_present = fake_crash_marker_present;
  fake->host.write_crash_marker = fake_write_crash_marker;
  fake->host.clear_crash_marker = fake_clear_crash_marker;
  fake->host.connect_broker = fake_connect_broker;
  fake->host.peer_token = fake_peer_token;
  fake->host.inspect_peer = fake_inspect_peer;
  fake->host.monotonic_ms = fake_monotonic_ms;
  fake->host.random_bytes = fake_random_bytes;
  fake->host.write_all = fake_write_all;
  fake->host.read_all = fake_read_all;
  fake->host.close_fd = fake_close_fd;
}
