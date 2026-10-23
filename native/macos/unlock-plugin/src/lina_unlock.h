// SPDX-License-Identifier: Apache-2.0
// The decision core of the locked-Mac unlock plug-in.
//
// The core owns the decision order, the deadline and the wire exchange. Every
// operating-system call it needs (Security, sockets, the monotonic clock and
// the files that hold the markers) sits behind `struct lina_host`, so the same
// core builds into the macOS bundle and into the Linux foundation check.
#ifndef LINA_UNLOCK_H
#define LINA_UNLOCK_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#define LINA_NONCE_BYTES 32
#define LINA_TICKET_BYTES 16
#define LINA_TOKEN_BYTES 32
#define LINA_SOCKET_PATH_MAX 104
#define LINA_DEFAULT_DEADLINE_MS 500u
#define LINA_MAX_DEADLINE_MS 500u

// One value per answer. The plug-in maps LINA_ALLOW to allow and every other
// value to deny; there is deliberately no undefined or cancelled value.
enum lina_decision {
  LINA_ALLOW = 0,
  LINA_DENY_NOT_ENABLED,
  LINA_DENY_CRASH_MARKER,
  LINA_DENY_NO_SOCKET,
  LINA_DENY_CONNECT_TIMEOUT,
  LINA_DENY_PEER_TOKEN,
  LINA_DENY_SIGNATURE,
  LINA_DENY_HARDENED_RUNTIME,
  LINA_DENY_DEBUG_ENTITLEMENT,
  LINA_DENY_WRONG_USER,
  LINA_DENY_NO_NONCE,
  LINA_DENY_IO,
  LINA_DENY_NO_REPLY,
  LINA_DENY_WRONG_NONCE,
  LINA_DENY_NO_TICKET,
  LINA_DENY_PEER_CHANGED,
  LINA_DENY_DEADLINE,
  LINA_DENY_INTERNAL,
  LINA_DECISION_COUNT
};

// Written by the installer: whether the feature is on for this Mac, where the
// broker socket of the person who turned it on lives, that person's uid and the
// deadline the eval set for this build.
struct lina_config {
  bool enabled;
  char socket_path[LINA_SOCKET_PATH_MAX];
  uint32_t person_uid;
  uint32_t deadline_ms;
};

// An audit token as `LOCAL_PEERTOKEN` returns it, plus the fields the host
// already decoded from it.
struct lina_token {
  uint8_t bytes[LINA_TOKEN_BYTES];
  uint32_t auid;
  uint32_t euid;
  int32_t pid;
};

// What the host learned about the peer's running code.
struct lina_peer_report {
  bool signature_ok;
  bool hardened_runtime;
  bool get_task_allow;
  bool same_user;
};

struct lina_host {
  void *context;
  // 0 on success; -1 when the install state cannot be read.
  int (*load_config)(void *context, struct lina_config *out);
  // 1 when a crashed evaluation left its marker, 0 when absent, -1 on error.
  int (*crash_marker_present)(void *context);
  // 0 on success.
  int (*write_crash_marker)(void *context);
  void (*clear_crash_marker)(void *context);
  // A connected socket, or -1. `timed_out` says the deadline expired first.
  int (*connect_broker)(void *context, const char *path, uint32_t timeout_ms, bool *timed_out);
  // 0 on success.
  int (*peer_token)(void *context, int fd, struct lina_token *out);
  // 0 on success; -1 when the host could not inspect the peer at all.
  int (*inspect_peer)(void *context, const struct lina_token *token, uint32_t person_uid,
                      struct lina_peer_report *out);
  uint64_t (*monotonic_ms)(void *context);
  // 0 on success.
  int (*random_bytes)(void *context, uint8_t *out, size_t len);
  // 0 when the whole buffer crossed the socket before the deadline.
  int (*write_all)(void *context, int fd, const void *buf, size_t len, uint32_t timeout_ms, bool *timed_out);
  int (*read_all)(void *context, int fd, void *buf, size_t len, uint32_t timeout_ms, bool *timed_out);
  void (*close_fd)(void *context, int fd);
};

struct lina_result {
  enum lina_decision decision;
  uint8_t ticket[LINA_TICKET_BYTES];
  uint32_t deadline_ms;
};

// Runs the whole decision in the order the design fixes. Every exit path
// clears the crash marker it wrote.
struct lina_result lina_decide(const struct lina_host *host);

// True only for LINA_ALLOW. The mechanism passes this value to SetResult, so no
// path can produce undefined or user cancelled.
bool lina_decision_allows(enum lina_decision decision);
const char *lina_decision_name(enum lina_decision decision);

// True for the mechanism id this plug-in owns: `unlock`, `unlock,privileged`,
// or the right's form `LinaUnlock:unlock,privileged`. The loader may hand the
// id with or without the plug-in prefix.
bool lina_mechanism_id_matches(const char *mechanism_id);

// The two operations a mechanism may use. Context and hint values are absent
// by construction: a credential cannot be carried through an interface that
// does not exist.
struct lina_engine {
  void *context;
  void (*set_result)(void *context, bool allow);
  void (*write_log)(void *context, const char *line);
};

// One decision, one log line, one result.
void lina_mechanism_invoke(const struct lina_host *host, const struct lina_engine *engine);

#endif
