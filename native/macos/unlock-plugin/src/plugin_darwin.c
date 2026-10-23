// SPDX-License-Identifier: Apache-2.0
// The authorization plug-in: one mechanism, no interface. macOS loads this
// bundle into the privileged plug-in host and calls MechanismInvoke while the
// lock screen's rule list is evaluated.
#include <Security/Authorization.h>
#include <Security/AuthorizationPlugin.h>
#include <stdlib.h>

#include "darwin_host.h"
#include "engine_callbacks.h"
#include "lina_unlock.h"

struct lina_plugin {
  const AuthorizationCallbacks *callbacks;
};

struct lina_mechanism {
  const AuthorizationCallbacks *callbacks;
  AuthorizationEngineRef engine;
};

static OSStatus plugin_destroy(AuthorizationPluginRef plugin) {
  free(plugin);
  return errAuthorizationSuccess;
}

static OSStatus mechanism_create(AuthorizationPluginRef plugin, AuthorizationEngineRef engine,
                                 AuthorizationMechanismId mechanism_id, AuthorizationMechanismRef *out) {
  const struct lina_plugin *state = (const struct lina_plugin *)plugin;
  if (!lina_mechanism_id_matches(mechanism_id)) return errAuthorizationInternal;
  struct lina_mechanism *mechanism = calloc(1, sizeof *mechanism);
  if (mechanism == NULL) return errAuthorizationInternal;
  mechanism->callbacks = state->callbacks;
  mechanism->engine = engine;
  *out = (AuthorizationMechanismRef)mechanism;
  return errAuthorizationSuccess;
}

static OSStatus mechanism_invoke(AuthorizationMechanismRef reference) {
  struct lina_mechanism *mechanism = (struct lina_mechanism *)reference;
  struct lina_callback_engine storage;
  struct lina_engine engine;
  lina_engine_from_callbacks(mechanism->callbacks, mechanism->engine, &storage, &engine);
  lina_mechanism_invoke(lina_darwin_host(), &engine);
  return errAuthorizationSuccess;
}

static OSStatus mechanism_deactivate(AuthorizationMechanismRef reference) {
  (void)reference;
  return errAuthorizationSuccess;
}

static OSStatus mechanism_destroy(AuthorizationMechanismRef reference) {
  free(reference);
  return errAuthorizationSuccess;
}

OSStatus AuthorizationPluginCreate(const AuthorizationCallbacks *callbacks, AuthorizationPluginRef *out_plugin,
                                   const AuthorizationPluginInterface **out_interface) {
  static const AuthorizationPluginInterface plugin_interface = {
    .version = kAuthorizationPluginInterfaceVersion,
    .PluginDestroy = plugin_destroy,
    .MechanismCreate = mechanism_create,
    .MechanismInvoke = mechanism_invoke,
    .MechanismDeactivate = mechanism_deactivate,
    .MechanismDestroy = mechanism_destroy,
  };
  if (callbacks == NULL || out_plugin == NULL || out_interface == NULL) return errAuthorizationInternal;
  struct lina_plugin *plugin = calloc(1, sizeof *plugin);
  if (plugin == NULL) return errAuthorizationInternal;
  plugin->callbacks = callbacks;
  *out_plugin = (AuthorizationPluginRef)plugin;
  *out_interface = &plugin_interface;
  return errAuthorizationSuccess;
}
