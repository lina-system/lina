// SPDX-License-Identifier: Apache-2.0
// The plug-in's engine, built from the engine callbacks macOS hands the
// plug-in. Only SetResult is taken from that table; the answer is written to
// the system log through os_log.
#ifndef LINA_ENGINE_CALLBACKS_H
#define LINA_ENGINE_CALLBACKS_H

#include <Security/AuthorizationPlugin.h>

#include "lina_unlock.h"

struct lina_callback_engine {
  const AuthorizationCallbacks *callbacks;
  AuthorizationEngineRef engine;
};

void lina_engine_from_callbacks(const AuthorizationCallbacks *callbacks, AuthorizationEngineRef engine,
                                struct lina_callback_engine *storage, struct lina_engine *out);

#endif
