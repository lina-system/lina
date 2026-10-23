// SPDX-License-Identifier: Apache-2.0
// The plug-in's engine, built from the engine callbacks macOS hands the
// plug-in. Only SetResult is taken from that table; the authorization engine
// has no log callback, so every answer goes to the system log through os_log.
#include <Security/AuthorizationPlugin.h>
#include <os/log.h>

#include "engine_callbacks.h"
#include "lina_unlock.h"

static void set_result(void *context, bool allow) {
  struct lina_callback_engine *state = context;
  state->callbacks->SetResult(state->engine, allow ? kAuthorizationResultAllow : kAuthorizationResultDeny);
}

static void write_log(void *context, const char *line) {
  (void)context;
  // Public so the ticket id and the reason stay readable in the system log;
  // the line carries no credential by construction.
  os_log_with_type(OS_LOG_DEFAULT, OS_LOG_TYPE_DEFAULT, "%{public}s", line);
}

void lina_engine_from_callbacks(const AuthorizationCallbacks *callbacks, AuthorizationEngineRef engine,
                                struct lina_callback_engine *storage, struct lina_engine *out) {
  storage->callbacks = callbacks;
  storage->engine = engine;
  out->context = storage;
  out->set_result = set_result;
  out->write_log = write_log;
}
