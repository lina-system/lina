// SPDX-License-Identifier: Apache-2.0
// The macOS host: the authorization database, the code checks, launchd, the
// session state and the root-only files. Everything here runs as root except
// the rollback request, which is a socket the guard serves.
#ifndef LINA_DARWIN_HOST_H
#define LINA_DARWIN_HOST_H

#include <stdbool.h>

#include "host.h"
#include "state.h"

struct lina_darwin {
  struct lina_host host;
  struct lina_paths paths;
};

// Fills `host` with the macOS implementation. Returns false when a required
// system call is unavailable on this build.
bool lina_darwin_host_init(struct lina_darwin *darwin, const struct lina_paths *paths);

// The guard's socket. Root owns it; the admin group reads and writes it, so the
// person's own account can ask for a restore without a password.
#define LINA_GUARD_SOCKET "/var/run/lina/locked-screen-guard.sock"

#endif
