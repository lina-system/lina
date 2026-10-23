// SPDX-License-Identifier: Apache-2.0
// A host whose every step is scripted, plus an injected monotonic clock. The
// tests use it to drive each deny path and to hold the deadline without
// sleeping.
#ifndef LINA_FAKE_HOST_H
#define LINA_FAKE_HOST_H

#include <stdbool.h>
#include <stdint.h>

#include "lina_unlock.h"

struct fake_host {
  struct lina_host host;
  struct lina_config config;
  int load_config_status;
  uint32_t load_config_delay_ms;

  int marker_present;
  int write_marker_status;
  uint32_t write_marker_delay_ms;
  bool marker_written;
  bool marker_cleared;

  int connect_status;
  bool connect_timed_out;
  uint32_t connect_timeout;
  int connect_calls;

  int token_status;
  struct lina_token token;
  int second_token_status;
  struct lina_token second_token;
  bool second_token_read;

  int inspect_status;
  uint32_t inspect_delay_ms;
  struct lina_peer_report report;

  int random_status;
  uint8_t nonce[LINA_NONCE_BYTES];
  int random_calls;

  int write_status;
  bool write_timed_out;
  uint32_t write_timeout;

  int read_status;
  bool read_timed_out;
  uint32_t read_timeout;
  int read_calls;

  bool reply_bad_magic;
  bool reply_armed;
  bool reply_nonce_mismatch;
  uint8_t ticket[LINA_TICKET_BYTES];

  // Injected clock: every read returns `now` and then advances it by `tick_ms`.
  uint64_t now;
  uint64_t tick_ms;

  int close_calls;
};

// Every step succeeds and the reply carries the request's nonce, an armed
// ticket and an unchanged peer token.
void fake_host_init(struct fake_host *fake);

#endif
