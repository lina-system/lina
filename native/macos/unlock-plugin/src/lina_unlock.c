// SPDX-License-Identifier: Apache-2.0
// The decision core of the locked-Mac unlock plug-in.
//
// The order is the design's: the enabled marker, the crash marker, the broker
// socket, the peer's audit token, its signature and runtime flags, the person,
// a fresh nonce, a reply that carries the nonce and an armed ticket, and the
// audit token read again. Only then allow. Everything else is a deny, and the
// deadline bounds every host call.
#include <string.h>

#include "lina_unlock.h"
#include "protocol.h"

static uint32_t effective_deadline(uint32_t requested) {
  if (requested == 0) return 1;
  if (requested > LINA_MAX_DEADLINE_MS) return LINA_MAX_DEADLINE_MS;
  return requested;
}

static uint32_t remaining_ms(const struct lina_host *host, uint64_t limit) {
  uint64_t now = host->monotonic_ms(host->context);
  if (now >= limit) return 0;
  uint64_t left = limit - now;
  return left > (uint64_t)UINT32_MAX ? UINT32_MAX : (uint32_t)left;
}

static bool deadline_passed(const struct lina_host *host, uint64_t limit) {
  return host->monotonic_ms(host->context) >= limit;
}

static bool host_complete(const struct lina_host *host) {
  return host != NULL && host->load_config != NULL && host->crash_marker_present != NULL
      && host->write_crash_marker != NULL && host->clear_crash_marker != NULL
      && host->connect_broker != NULL && host->peer_token != NULL && host->inspect_peer != NULL
      && host->monotonic_ms != NULL && host->random_bytes != NULL && host->write_all != NULL
      && host->read_all != NULL && host->close_fd != NULL;
}

static bool ticket_empty(const uint8_t ticket[LINA_TICKET_BYTES]) {
  for (size_t index = 0; index < LINA_TICKET_BYTES; index++) {
    if (ticket[index] != 0) return false;
  }
  return true;
}

struct lina_result lina_decide(const struct lina_host *host) {
  struct lina_result result;
  memset(&result, 0, sizeof result);
  result.decision = LINA_DENY_INTERNAL;
  if (!host_complete(host)) return result;

  // The deadline bounds the whole evaluation, so the clock starts before the
  // install state is read and every later host call receives what is left.
  const uint64_t start = host->monotonic_ms(host->context);
  struct lina_config config;
  memset(&config, 0, sizeof config);
  if (host->load_config(host->context, &config) != 0) return result;
  result.deadline_ms = effective_deadline(config.deadline_ms);
  const uint64_t limit = start + result.deadline_ms;

  if (!config.enabled) {
    result.decision = LINA_DENY_NOT_ENABLED;
    return result;
  }
  int marker = host->crash_marker_present(host->context);
  if (marker < 0) return result;
  if (marker > 0) {
    result.decision = LINA_DENY_CRASH_MARKER;
    return result;
  }
  if (host->write_crash_marker(host->context) != 0) return result;

  enum lina_decision decision = LINA_DENY_INTERNAL;
  uint8_t ticket[LINA_TICKET_BYTES];
  uint8_t nonce[LINA_NONCE_BYTES];
  uint8_t request[LINA_REQUEST_BYTES];
  uint8_t reply[LINA_REPLY_BYTES];
  int fd = -1;
  bool timed_out = false;
  memset(ticket, 0, sizeof ticket);
  memset(nonce, 0, sizeof nonce);

  do {
    uint32_t left = remaining_ms(host, limit);
    if (left == 0) {
      decision = LINA_DENY_DEADLINE;
      break;
    }
    fd = host->connect_broker(host->context, config.socket_path, left, &timed_out);
    if (fd < 0) {
      decision = timed_out ? LINA_DENY_CONNECT_TIMEOUT : LINA_DENY_NO_SOCKET;
      break;
    }

    struct lina_token before;
    memset(&before, 0, sizeof before);
    if (host->peer_token(host->context, fd, &before) != 0) {
      decision = LINA_DENY_PEER_TOKEN;
      break;
    }

    struct lina_peer_report report;
    memset(&report, 0, sizeof report);
    if (host->inspect_peer(host->context, &before, config.person_uid, &report) != 0) {
      decision = LINA_DENY_INTERNAL;
      break;
    }
    if (remaining_ms(host, limit) == 0) {
      decision = LINA_DENY_DEADLINE;
      break;
    }
    if (!report.signature_ok) {
      decision = LINA_DENY_SIGNATURE;
      break;
    }
    if (!report.hardened_runtime) {
      decision = LINA_DENY_HARDENED_RUNTIME;
      break;
    }
    if (report.get_task_allow) {
      decision = LINA_DENY_DEBUG_ENTITLEMENT;
      break;
    }
    if (!report.same_user) {
      decision = LINA_DENY_WRONG_USER;
      break;
    }

    if (host->random_bytes(host->context, nonce, sizeof nonce) != 0) {
      decision = LINA_DENY_NO_NONCE;
      break;
    }

    memcpy(request, LINA_REQUEST_MAGIC, 4);
    request[4] = (uint8_t)LINA_PROTOCOL_VERSION;
    request[5] = 0;
    request[6] = 0;
    request[7] = 0;
    memcpy(request + LINA_REQUEST_NONCE_OFFSET, nonce, LINA_NONCE_BYTES);

    left = remaining_ms(host, limit);
    if (left == 0) {
      decision = LINA_DENY_DEADLINE;
      break;
    }
    if (host->write_all(host->context, fd, request, sizeof request, left, &timed_out) != 0) {
      decision = timed_out ? LINA_DENY_DEADLINE : LINA_DENY_IO;
      break;
    }

    left = remaining_ms(host, limit);
    if (left == 0) {
      decision = LINA_DENY_DEADLINE;
      break;
    }
    if (host->read_all(host->context, fd, reply, sizeof reply, left, &timed_out) != 0) {
      decision = timed_out ? (deadline_passed(host, limit) ? LINA_DENY_DEADLINE : LINA_DENY_NO_REPLY)
                           : LINA_DENY_IO;
      break;
    }
    if (deadline_passed(host, limit)) {
      decision = LINA_DENY_DEADLINE;
      break;
    }

    if (memcmp(reply, LINA_REPLY_MAGIC, 4) != 0 || reply[4] != (uint8_t)LINA_PROTOCOL_VERSION) {
      decision = LINA_DENY_INTERNAL;
      break;
    }
    if (memcmp(reply + LINA_REPLY_NONCE_OFFSET, nonce, LINA_NONCE_BYTES) != 0) {
      decision = LINA_DENY_WRONG_NONCE;
      break;
    }
    if ((reply[5] & (uint8_t)LINA_REPLY_ARMED) == 0) {
      decision = LINA_DENY_NO_TICKET;
      break;
    }
    memcpy(ticket, reply + LINA_REPLY_TICKET_OFFSET, LINA_TICKET_BYTES);
    if (ticket_empty(ticket)) {
      decision = LINA_DENY_NO_TICKET;
      break;
    }

    struct lina_token after;
    memset(&after, 0, sizeof after);
    if (host->peer_token(host->context, fd, &after) != 0) {
      decision = LINA_DENY_PEER_TOKEN;
      break;
    }
    if (remaining_ms(host, limit) == 0) {
      decision = LINA_DENY_DEADLINE;
      break;
    }
    if (memcmp(before.bytes, after.bytes, LINA_TOKEN_BYTES) != 0) {
      decision = LINA_DENY_PEER_CHANGED;
      break;
    }
    decision = LINA_ALLOW;
  } while (0);

  if (fd >= 0) host->close_fd(host->context, fd);
  host->clear_crash_marker(host->context);
  result.decision = decision;
  if (decision == LINA_ALLOW) memcpy(result.ticket, ticket, LINA_TICKET_BYTES);
  return result;
}

bool lina_decision_allows(enum lina_decision decision) {
  return decision == LINA_ALLOW;
}

const char *lina_decision_name(enum lina_decision decision) {
  switch (decision) {
    case LINA_ALLOW: return "allow";
    case LINA_DENY_NOT_ENABLED: return "deny:not-enabled";
    case LINA_DENY_CRASH_MARKER: return "deny:crash-marker";
    case LINA_DENY_NO_SOCKET: return "deny:no-socket";
    case LINA_DENY_CONNECT_TIMEOUT: return "deny:connect-timeout";
    case LINA_DENY_PEER_TOKEN: return "deny:peer-token";
    case LINA_DENY_SIGNATURE: return "deny:signature";
    case LINA_DENY_HARDENED_RUNTIME: return "deny:hardened-runtime";
    case LINA_DENY_DEBUG_ENTITLEMENT: return "deny:debug-entitlement";
    case LINA_DENY_WRONG_USER: return "deny:wrong-user";
    case LINA_DENY_NO_NONCE: return "deny:no-nonce";
    case LINA_DENY_IO: return "deny:io";
    case LINA_DENY_NO_REPLY: return "deny:no-reply";
    case LINA_DENY_WRONG_NONCE: return "deny:wrong-nonce";
    case LINA_DENY_NO_TICKET: return "deny:no-ticket";
    case LINA_DENY_PEER_CHANGED: return "deny:peer-changed";
    case LINA_DENY_DEADLINE: return "deny:deadline";
    case LINA_DENY_INTERNAL: return "deny:internal";
    case LINA_DECISION_COUNT: break;
  }
  return "deny:internal";
}
