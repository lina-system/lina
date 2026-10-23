// SPDX-License-Identifier: Apache-2.0
// Install and uninstall of the lock screen rule, in the order the design fixes.
#ifndef LINA_INSTALL_H
#define LINA_INSTALL_H

#include <stdint.h>

#include "host.h"
#include "state.h"

enum lina_install_code {
  LINA_INSTALL_OK = 0,
  LINA_INSTALL_DEVICE,
  LINA_INSTALL_BUILD,
  LINA_INSTALL_RULE,
  LINA_INSTALL_BACKUP,
  LINA_INSTALL_BUNDLE,
  LINA_INSTALL_SIGNATURE,
  LINA_INSTALL_GUARD,
  LINA_INSTALL_RIGHT,
  LINA_INSTALL_SELFTEST,
  LINA_INSTALL_READBACK,
  LINA_INSTALL_STATE,
};

struct lina_outcome {
  int code;
  // A short, stable label for logs: `platform-sso`, `bundle-missing`, ...
  const char *reason;
};

struct lina_install_input {
  // The signed plug-in bundle the release carries.
  const char *bundle_source;
  // The guard binary and its launchd job from the same release. Optional only
  // for the portable tests.
  const char *guard_binary;
  const char *guard_plist;
  // The builds the eval passed on. Copied into the state so the guard reads it.
  const char *measured_source;
  // What the plug-in needs to reach the broker of the person who turned it on.
  const char *broker_socket;
  uint32_t person_uid;
  uint32_t deadline_ms;
};

// Writes the backup, copies and checks the bundle, writes the two rights and
// the rule, then leaves a pending record for the manual unlock. Every failure
// removes what this attempt wrote and leaves the rule as it was found.
struct lina_outcome lina_install(const struct lina_host *host, const struct lina_paths *paths,
                                 const struct lina_install_input *input);

enum lina_uninstall_code {
  LINA_UNINSTALL_OK = 0,
  // A rule someone else wrote still names LINA's right. Nothing that rule needs
  // was removed; the report says so.
  LINA_UNINSTALL_DEFERRED,
  LINA_UNINSTALL_FAILED,
};

// Turns the feature off, restores the backup when the rule is LINA's, then
// removes LINA's rights, the bundle and the state. It never leaves a rule
// naming a missing plug-in.
int lina_uninstall(const struct lina_host *host, const struct lina_paths *paths, struct lina_outcome *outcome);

#endif
