// SPDX-License-Identifier: Apache-2.0
// MechanismDeactivate must call DidDeactivate. Returning success without that
// call leaves the authorization engine waiting.
#include <stdio.h>
#include <stdlib.h>

#include <Security/Authorization.h>
#include <Security/AuthorizationPlugin.h>

#include "darwin_host.h"
#include "test.h"

static int set_results;
static int forbidden_calls;
static int deactivations;
static AuthorizationEngineRef deactivated_engine;
static OSStatus deactivate_status;

static OSStatus record_result(AuthorizationEngineRef engine, AuthorizationResult result) {
  (void)engine;
  (void)result;
  set_results++;
  return errAuthorizationSuccess;
}

static OSStatus record_deactivate(AuthorizationEngineRef engine) {
  deactivations++;
  deactivated_engine = engine;
  return deactivate_status;
}

static OSStatus forbidden(AuthorizationEngineRef engine) {
  (void)engine;
  forbidden_calls++;
  return errAuthorizationSuccess;
}

static OSStatus forbidden_context(AuthorizationEngineRef engine, AuthorizationString key,
                                  AuthorizationContextFlags *flags, const AuthorizationValue **value) {
  (void)key;
  (void)flags;
  (void)value;
  return forbidden(engine);
}

static OSStatus forbidden_set_context(AuthorizationEngineRef engine, AuthorizationString key,
                                      AuthorizationContextFlags flags, const AuthorizationValue *value) {
  (void)key;
  (void)flags;
  (void)value;
  return forbidden(engine);
}

static OSStatus forbidden_hint(AuthorizationEngineRef engine, AuthorizationString key,
                               const AuthorizationValue **value) {
  (void)key;
  (void)value;
  return forbidden(engine);
}

static OSStatus forbidden_set_hint(AuthorizationEngineRef engine, AuthorizationString key,
                                   const AuthorizationValue *value) {
  (void)key;
  (void)value;
  return forbidden(engine);
}

static OSStatus forbidden_arguments(AuthorizationEngineRef engine, const AuthorizationValueVector **arguments) {
  (void)arguments;
  return forbidden(engine);
}

// The entry-point tests never invoke the mechanism, so the macOS host is not
// linked. A call here means a test reached MechanismInvoke.
const struct lina_host *lina_darwin_host(void) {
  fprintf(stderr, "plug-in entry tests must not invoke the mechanism\n");
  abort();
}

static AuthorizationCallbacks callbacks_with_deactivate(void) {
  AuthorizationCallbacks callbacks = {
    .version = 4,
    .SetResult = record_result,
    .DidDeactivate = record_deactivate,
    .GetContextValue = forbidden_context,
    .SetContextValue = forbidden_set_context,
    .GetHintValue = forbidden_hint,
    .SetHintValue = forbidden_set_hint,
    .GetArguments = forbidden_arguments,
  };
  return callbacks;
}

static void reset_counters(void) {
  set_results = 0;
  forbidden_calls = 0;
  deactivations = 0;
  deactivated_engine = NULL;
  deactivate_status = errAuthorizationSuccess;
}

static OSStatus create_mechanism(const AuthorizationCallbacks *callbacks, AuthorizationEngineRef engine,
                                 AuthorizationMechanismRef *mechanism, const AuthorizationPluginInterface **interface,
                                 AuthorizationPluginRef *plugin) {
  OSStatus status = AuthorizationPluginCreate(callbacks, plugin, interface);
  if (status != errAuthorizationSuccess) return status;
  return (*interface)->MechanismCreate(*plugin, engine, "LinaUnlock:unlock,privileged", mechanism);
}

static void test_deactivate_calls_did_deactivate(void) {
  static int engine_mark;
  AuthorizationEngineRef engine = (AuthorizationEngineRef)&engine_mark;
  AuthorizationCallbacks callbacks = callbacks_with_deactivate();
  AuthorizationPluginRef plugin = NULL;
  const AuthorizationPluginInterface *interface = NULL;
  AuthorizationMechanismRef mechanism = NULL;
  reset_counters();
  deactivate_status = errAuthorizationSuccess;
  OSStatus status = create_mechanism(&callbacks, engine, &mechanism, &interface, &plugin);
  CHECK(status == errAuthorizationSuccess);
  status = interface->MechanismDeactivate(mechanism);
  CHECK(status == errAuthorizationSuccess);
  CHECK(deactivations == 1);
  CHECK(deactivated_engine == engine);
  CHECK(set_results == 0);
  CHECK(forbidden_calls == 0);
  interface->MechanismDestroy(mechanism);
  CHECK(deactivations == 1);
  interface->PluginDestroy(plugin);
}

static void test_deactivate_returns_callback_status(void) {
  static int engine_mark;
  AuthorizationCallbacks callbacks = callbacks_with_deactivate();
  AuthorizationPluginRef plugin = NULL;
  const AuthorizationPluginInterface *interface = NULL;
  AuthorizationMechanismRef mechanism = NULL;
  reset_counters();
  deactivate_status = errAuthorizationInternal;
  OSStatus status = create_mechanism(&callbacks, (AuthorizationEngineRef)&engine_mark, &mechanism, &interface, &plugin);
  CHECK(status == errAuthorizationSuccess);
  status = interface->MechanismDeactivate(mechanism);
  CHECK(status == errAuthorizationInternal);
  CHECK(deactivations == 1);
  interface->MechanismDestroy(mechanism);
  interface->PluginDestroy(plugin);
}

static void test_deactivate_without_callback(void) {
  static int engine_mark;
  AuthorizationCallbacks callbacks = callbacks_with_deactivate();
  callbacks.DidDeactivate = NULL;
  AuthorizationPluginRef plugin = NULL;
  const AuthorizationPluginInterface *interface = NULL;
  AuthorizationMechanismRef mechanism = NULL;
  reset_counters();
  OSStatus status = create_mechanism(&callbacks, (AuthorizationEngineRef)&engine_mark, &mechanism, &interface, &plugin);
  CHECK(status == errAuthorizationSuccess);
  status = interface->MechanismDeactivate(mechanism);
  CHECK(status == errAuthorizationInternal);
  CHECK(deactivations == 0);
  interface->MechanismDestroy(mechanism);
  interface->PluginDestroy(plugin);
}

static void test_unknown_mechanism_is_not_created(void) {
  AuthorizationCallbacks callbacks = callbacks_with_deactivate();
  AuthorizationPluginRef plugin = NULL;
  const AuthorizationPluginInterface *interface = NULL;
  AuthorizationMechanismRef mechanism = NULL;
  reset_counters();
  OSStatus status = AuthorizationPluginCreate(&callbacks, &plugin, &interface);
  CHECK(status == errAuthorizationSuccess);
  status = interface->MechanismCreate(plugin, NULL, "lock,privileged", &mechanism);
  CHECK(status == errAuthorizationInternal);
  CHECK(mechanism == NULL);
  CHECK(deactivations == 0);
  interface->PluginDestroy(plugin);
}

void test_plugin(void) {
  test_deactivate_calls_did_deactivate();
  test_deactivate_returns_callback_status();
  test_deactivate_without_callback();
  test_unknown_mechanism_is_not_created();
}
