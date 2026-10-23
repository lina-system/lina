// SPDX-License-Identifier: Apache-2.0
// A test stand-in for Apple's <Security/AuthorizationPlugin.h>. It declares
// only the members the plug-in may touch, plus the context, hint and argument
// readers the plug-in must never call. The fake build therefore fails to
// compile if the plug-in reaches for anything else, and the tests install
// tripwires on the forbidden members.
#ifndef LINA_FAKE_AUTHORIZATION_PLUGIN_H
#define LINA_FAKE_AUTHORIZATION_PLUGIN_H

#include <stdint.h>

typedef int32_t OSStatus;
typedef int32_t AuthorizationResult;

typedef struct AuthorizationEngineRefOpaque *AuthorizationEngineRef;
typedef struct AuthorizationPluginRefOpaque *AuthorizationPluginRef;
typedef struct AuthorizationMechanismRefOpaque *AuthorizationMechanismRef;
typedef const char *AuthorizationMechanismId;

enum {
  kAuthorizationResultAllow = 0,
  kAuthorizationResultDeny = 1,
  kAuthorizationResultUndefined = 2,
  kAuthorizationResultUserCanceled = 3
};

typedef struct AuthorizationValueVector {
  unsigned long count;
  char **values;
} AuthorizationValueVector;

typedef struct AuthorizationCallbacks {
  OSStatus (*SetResult)(AuthorizationEngineRef engine, AuthorizationResult result);
  // The engine waits on this after MechanismDeactivate. Returning success from
  // the mechanism does not finish that handshake.
  OSStatus (*DidDeactivate)(AuthorizationEngineRef engine);
  void (*WriteLog)(AuthorizationEngineRef engine, const char *format, ...);
  OSStatus (*GetContextValue)(AuthorizationEngineRef engine, const char *key, const void **value);
  OSStatus (*SetContextValue)(AuthorizationEngineRef engine, const char *key, const void *value);
  OSStatus (*GetHint)(AuthorizationEngineRef engine, const char *key, const void **value);
  OSStatus (*SetHint)(AuthorizationEngineRef engine, const char *key, const void *value);
  OSStatus (*GetArguments)(AuthorizationEngineRef engine, const AuthorizationValueVector **arguments);
} AuthorizationCallbacks;

enum { kAuthorizationPluginInterfaceVersion = 0 };

typedef struct AuthorizationPluginInterface {
  uint32_t version;
  OSStatus (*PluginDestroy)(AuthorizationPluginRef plugin);
  OSStatus (*MechanismCreate)(AuthorizationPluginRef plugin, AuthorizationEngineRef engine,
                              AuthorizationMechanismId mechanism_id, AuthorizationMechanismRef *out);
  OSStatus (*MechanismInvoke)(AuthorizationMechanismRef reference);
  OSStatus (*MechanismDeactivate)(AuthorizationMechanismRef reference);
  OSStatus (*MechanismDestroy)(AuthorizationMechanismRef reference);
} AuthorizationPluginInterface;

OSStatus AuthorizationPluginCreate(const AuthorizationCallbacks *callbacks, AuthorizationPluginRef *out_plugin,
                                   const AuthorizationPluginInterface **out_interface);

#endif
