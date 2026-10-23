// SPDX-License-Identifier: Apache-2.0
// One evaluation: one decision, one log line and one result. The mechanism
// never returns undefined or user cancelled, because the engine maps every
// decision to allow or deny before it calls SetResult.
#include <stdio.h>
#include <string.h>

#include "lina_unlock.h"

#define LINA_BUNDLE_NAME "LinaUnlock"
#define LINA_MECHANISM_NAME "unlock"

static void ticket_text(const uint8_t ticket[LINA_TICKET_BYTES], char out[LINA_TICKET_BYTES * 2 + 1]) {
  static const char digits[] = "0123456789abcdef";
  bool empty = true;
  for (size_t index = 0; index < LINA_TICKET_BYTES; index++) {
    if (ticket[index] != 0) empty = false;
  }
  if (empty) {
    out[0] = '-';
    out[1] = '\0';
    return;
  }
  for (size_t index = 0; index < LINA_TICKET_BYTES; index++) {
    out[index * 2] = digits[ticket[index] >> 4];
    out[index * 2 + 1] = digits[ticket[index] & 0x0fu];
  }
  out[LINA_TICKET_BYTES * 2] = '\0';
}

bool lina_mechanism_id_matches(const char *mechanism_id) {
  if (mechanism_id == NULL) return false;
  const char *name = mechanism_id;
  const char *colon = strchr(mechanism_id, ':');
  if (colon != NULL) {
    size_t prefix = (size_t)(colon - mechanism_id);
    if (prefix != strlen(LINA_BUNDLE_NAME) || strncmp(mechanism_id, LINA_BUNDLE_NAME, prefix) != 0) return false;
    name = colon + 1;
  }
  const char *comma = strchr(name, ',');
  size_t length = comma != NULL ? (size_t)(comma - name) : strlen(name);
  if (length != strlen(LINA_MECHANISM_NAME) || strncmp(name, LINA_MECHANISM_NAME, length) != 0) return false;
  if (comma != NULL && strcmp(comma, ",privileged") != 0) return false;
  return true;
}

void lina_mechanism_invoke(const struct lina_host *host, const struct lina_engine *engine) {
  struct lina_result result = lina_decide(host);
  char ticket[LINA_TICKET_BYTES * 2 + 1];
  char line[192];
  ticket_text(result.ticket, ticket);
  snprintf(line, sizeof line, "lina unlock: ticket=%s decision=%s deadline_ms=%u", ticket,
           lina_decision_name(result.decision), (unsigned)result.deadline_ms);
  if (engine->write_log != NULL) engine->write_log(engine->context, line);
  if (engine->set_result != NULL) engine->set_result(engine->context, lina_decision_allows(result.decision));
}
