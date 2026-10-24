// SPDX-License-Identifier: Apache-2.0
// The restore request. The rollback command sends one frame to the guard when it
// runs as the person; the guard answers with one frame. Both sides are fixed
// sizes, so neither side can be talked into a different operation.
#ifndef LINA_RESTORE_H
#define LINA_RESTORE_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "host.h"
#include "state.h"

#define LINA_RESTORE_FRAME 16
#define LINA_RESTORE_VERSION 1

enum lina_restore_status {
  LINA_RESTORE_OK = 0,
  LINA_RESTORE_FAILED,
  LINA_RESTORE_REFUSED,
};

void lina_restore_request_encode(uint8_t out[LINA_RESTORE_FRAME]);
// 0 when the frame is exactly one version 1 restore request.
int lina_restore_request_decode(const uint8_t frame[LINA_RESTORE_FRAME]);
void lina_restore_reply_encode(enum lina_restore_status status, uint8_t out[LINA_RESTORE_FRAME]);
// -1 when the frame is not a version 1 reply.
int lina_restore_reply_decode(const uint8_t frame[LINA_RESTORE_FRAME], enum lina_restore_status *status);
const char *lina_restore_status_name(enum lina_restore_status status);

// What the rollback command does once it has a host: either it is root and
// restores directly, or it is the person and sends the request. `send_request`
// is NULL when the caller has no socket transport; the portable test passes a
// function that plays the guard's side against a second host.
struct lina_rollback {
  void *context;
  // The request frame, written by `send_request`; its reply must arrive in the
  // same call.
  int (*send_request)(void *context, const uint8_t request[LINA_RESTORE_FRAME],
                      uint8_t reply[LINA_RESTORE_FRAME], bool *refused);
};

enum lina_rollback_result {
  LINA_ROLLBACK_RESTORED = 0,
  LINA_ROLLBACK_REFUSED,
  LINA_ROLLBACK_FAILED,
};

// Runs the rollback. As root it restores through `host`. Otherwise it asks the
// guard through `request`. It never needs Node, Core or the network.
enum lina_rollback_result lina_rollback_run(const struct lina_host *host, const struct lina_paths *paths,
                                            bool root, const struct lina_rollback *request);

// The guard's side: answer one request frame. A request that is not exactly one
// version 1 restore frame is refused without touching anything.
void lina_restore_handle(const struct lina_host *host, const struct lina_paths *paths,
                         const uint8_t request[LINA_RESTORE_FRAME], uint8_t reply[LINA_RESTORE_FRAME]);

#endif
