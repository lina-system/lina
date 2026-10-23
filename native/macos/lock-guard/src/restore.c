// SPDX-License-Identifier: Apache-2.0
#include "restore.h"

#include <string.h>

#include "guard.h"

#define RESTORE_MAGIC "LNRB1"
#define REPLY_MAGIC "LNRB2"
#define RESTORE_ACTION 0x01u

void lina_restore_request_encode(uint8_t out[LINA_RESTORE_FRAME]) {
  memset(out, 0, LINA_RESTORE_FRAME);
  memcpy(out, RESTORE_MAGIC, 5);
  out[5] = LINA_RESTORE_VERSION;
  out[6] = RESTORE_ACTION;
}

int lina_restore_request_decode(const uint8_t frame[LINA_RESTORE_FRAME]) {
  if (memcmp(frame, RESTORE_MAGIC, 5) != 0) return -1;
  if (frame[5] != LINA_RESTORE_VERSION) return -1;
  if (frame[6] != RESTORE_ACTION) return -1;
  for (int i = 7; i < LINA_RESTORE_FRAME; i++) {
    if (frame[i] != 0) return -1;
  }
  return 0;
}

void lina_restore_reply_encode(enum lina_restore_status status, uint8_t out[LINA_RESTORE_FRAME]) {
  memset(out, 0, LINA_RESTORE_FRAME);
  memcpy(out, REPLY_MAGIC, 5);
  out[5] = LINA_RESTORE_VERSION;
  out[6] = (uint8_t)status;
}

int lina_restore_reply_decode(const uint8_t frame[LINA_RESTORE_FRAME], enum lina_restore_status *status) {
  if (memcmp(frame, REPLY_MAGIC, 5) != 0) return -1;
  if (frame[5] != LINA_RESTORE_VERSION) return -1;
  if (frame[6] > (uint8_t)LINA_RESTORE_REFUSED) return -1;
  for (int i = 7; i < LINA_RESTORE_FRAME; i++) {
    if (frame[i] != 0) return -1;
  }
  *status = (enum lina_restore_status)frame[6];
  return 0;
}

const char *lina_restore_status_name(enum lina_restore_status status) {
  switch (status) {
    case LINA_RESTORE_OK: return "ok";
    case LINA_RESTORE_FAILED: return "failed";
    case LINA_RESTORE_REFUSED: return "refused";
  }
  return "unknown";
}

void lina_restore_handle(const struct lina_host *host, const struct lina_paths *paths,
                         const uint8_t request[LINA_RESTORE_FRAME], uint8_t reply[LINA_RESTORE_FRAME]) {
  if (lina_restore_request_decode(request) != 0) {
    lina_restore_reply_encode(LINA_RESTORE_REFUSED, reply);
    return;
  }
  struct lina_guard_report report = lina_guard_restore(host, paths, "rollback-request");
  lina_restore_reply_encode(report.action == LINA_GUARD_RESTORED ? LINA_RESTORE_OK : LINA_RESTORE_FAILED, reply);
}

enum lina_rollback_result lina_rollback_run(const struct lina_host *host, const struct lina_paths *paths,
                                            bool root, const struct lina_rollback *request) {
  if (root) {
    struct lina_guard_report report = lina_guard_restore(host, paths, "rollback");
    return report.action == LINA_GUARD_RESTORED ? LINA_ROLLBACK_RESTORED : LINA_ROLLBACK_FAILED;
  }
  if (request == NULL || request->send_request == NULL) return LINA_ROLLBACK_REFUSED;
  uint8_t outgoing[LINA_RESTORE_FRAME];
  uint8_t incoming[LINA_RESTORE_FRAME];
  bool refused = false;
  lina_restore_request_encode(outgoing);
  if (request->send_request(request->context, outgoing, incoming, &refused) != 0) {
    return refused ? LINA_ROLLBACK_REFUSED : LINA_ROLLBACK_FAILED;
  }
  enum lina_restore_status status = LINA_RESTORE_FAILED;
  if (lina_restore_reply_decode(incoming, &status) != 0) return LINA_ROLLBACK_FAILED;
  if (status == LINA_RESTORE_OK) return LINA_ROLLBACK_RESTORED;
  return status == LINA_RESTORE_REFUSED ? LINA_ROLLBACK_REFUSED : LINA_ROLLBACK_FAILED;
}
