// SPDX-License-Identifier: Apache-2.0
#include <Security/AuthorizationPlugin.h>

#include "engine_callbacks.h"
#include "lina_unlock.h"

static void set_result(void *context, bool allow) {
  struct lina_callback_engine *state = context;
  state->callbacks->SetResult(state->engine, allow ? kAuthorizationResultAllow : kAuthorizationResultDeny);
}

static void write_log(void *context, const char *line) {
  struct lina_callback_engine *state = context;
  state->callbacks->WriteLog(state->engine, "%s", line);
}

void lina_engine_from_callbacks(const AuthorizationCallbacks *callbacks, AuthorizationEngineRef engine,
                                struct lina_callback_engine *storage, struct lina_engine *out) {
  storage->callbacks = callbacks;
  storage->engine = engine;
  out->context = storage;
  out->set_result = set_result;
  out->write_log = write_log;
}
