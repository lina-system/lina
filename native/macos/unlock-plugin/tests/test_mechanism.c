// SPDX-License-Identifier: Apache-2.0
// The plug-in answers through SetResult alone. These tests run the mechanism
// against a callbacks table whose context, hint and argument readers abort the
// evaluation if they are ever touched.
#include <stdarg.h>
#include <stdio.h>
#include <string.h>

#include <Security/AuthorizationPlugin.h>

#include "engine_callbacks.h"
#include "fake_host.h"
#include "lina_unlock.h"
#include "test.h"

static int set_results;
static int allow_results;
static int deny_results;
static int undefined_results;
static AuthorizationResult last_result;
static int log_lines;
static char last_log[512];
static int forbidden_calls;

static OSStatus record_result(AuthorizationEngineRef engine, AuthorizationResult result) {
  (void)engine;
  set_results++;
  last_result = result;
  if (result == kAuthorizationResultAllow) allow_results++;
  else if (result == kAuthorizationResultDeny) deny_results++;
  else undefined_results++;
  return 0;
}

static void record_log(AuthorizationEngineRef engine, const char *format, ...) {
  (void)engine;
  va_list arguments;
  va_start(arguments, format);
  vsnprintf(last_log, sizeof last_log, format, arguments);
  va_end(arguments);
  log_lines++;
}

static OSStatus forbidden_context(AuthorizationEngineRef engine, const char *key, const void **value) {
  (void)engine;
  (void)key;
  (void)value;
  forbidden_calls++;
  return 0;
}

static OSStatus forbidden_set_context(AuthorizationEngineRef engine, const char *key, const void *value) {
  (void)engine;
  (void)key;
  (void)value;
  forbidden_calls++;
  return 0;
}

static OSStatus forbidden_hint(AuthorizationEngineRef engine, const char *key, const void **value) {
  (void)engine;
  (void)key;
  (void)value;
  forbidden_calls++;
  return 0;
}

static OSStatus forbidden_set_hint(AuthorizationEngineRef engine, const char *key, const void *value) {
  (void)engine;
  (void)key;
  (void)value;
  forbidden_calls++;
  return 0;
}

static OSStatus forbidden_arguments(AuthorizationEngineRef engine, const AuthorizationValueVector **arguments) {
  (void)engine;
  (void)arguments;
  forbidden_calls++;
  return 0;
}

static const AuthorizationCallbacks callbacks = {
  .SetResult = record_result,
  .WriteLog = record_log,
  .GetContextValue = forbidden_context,
  .SetContextValue = forbidden_set_context,
  .GetHint = forbidden_hint,
  .SetHint = forbidden_set_hint,
  .GetArguments = forbidden_arguments,
};

static void reset_counters(void) {
  set_results = 0;
  allow_results = 0;
  deny_results = 0;
  undefined_results = 0;
  last_result = -1;
  log_lines = 0;
  last_log[0] = '\0';
  forbidden_calls = 0;
}

static void run_mechanism(struct fake_host *fake, void (*setup)(struct fake_host *)) {
  struct lina_callback_engine storage;
  struct lina_engine engine;
  fake_host_init(fake);
  if (setup != NULL) setup(fake);
  lina_engine_from_callbacks(&callbacks, (AuthorizationEngineRef)0, &storage, &engine);
  lina_mechanism_invoke(&fake->host, &engine);
}

static void not_enabled(struct fake_host *fake) { fake->config.enabled = false; }

static void test_allow_answer(void) {
  struct fake_host fake;
  reset_counters();
  run_mechanism(&fake, NULL);
  CHECK(set_results == 1);
  CHECK(allow_results == 1);
  CHECK(deny_results == 0);
  CHECK(undefined_results == 0);
  CHECK(last_result == kAuthorizationResultAllow);
  CHECK(log_lines == 1);
  CHECK(strstr(last_log, "ticket=") != NULL);
  CHECK(strstr(last_log, "decision=allow") != NULL);
  CHECK(strstr(last_log, "101112131415161718191a1b1c1d1e1f") != NULL);
  CHECK(forbidden_calls == 0);
}

static void test_deny_answer(void) {
  struct fake_host fake;
  reset_counters();
  run_mechanism(&fake, not_enabled);
  CHECK(set_results == 1);
  CHECK(deny_results == 1);
  CHECK(allow_results == 0);
  CHECK(undefined_results == 0);
  CHECK(last_result == kAuthorizationResultDeny);
  CHECK(log_lines == 1);
  CHECK(strstr(last_log, "decision=deny:not-enabled") != NULL);
  CHECK(strstr(last_log, "ticket=-") != NULL);
  CHECK(forbidden_calls == 0);
}

static void test_mechanism_identity(void) {
  CHECK(lina_mechanism_id_matches("unlock"));
  CHECK(lina_mechanism_id_matches("unlock,privileged"));
  CHECK(lina_mechanism_id_matches("LinaUnlock:unlock,privileged"));
  CHECK(lina_mechanism_id_matches("LinaUnlock:unlock"));
  CHECK(!lina_mechanism_id_matches("lock,privileged"));
  CHECK(!lina_mechanism_id_matches("unlock,debug"));
  CHECK(!lina_mechanism_id_matches("other:unlock,privileged"));
  CHECK(!lina_mechanism_id_matches("LinaUnlock:lock,privileged"));
  CHECK(!lina_mechanism_id_matches(""));
  CHECK(!lina_mechanism_id_matches(NULL));
}

static void test_only_two_answers(void) {
  for (int value = 0; value < (int)LINA_DECISION_COUNT; value++) {
    enum lina_decision decision = (enum lina_decision)value;
    if (decision == LINA_ALLOW) CHECK(lina_decision_allows(decision));
    else CHECK(!lina_decision_allows(decision));
    CHECK(strcmp(lina_decision_name(decision), "stub") != 0);
  }
}

void test_mechanism(void) {
  test_allow_answer();
  test_deny_answer();
  test_mechanism_identity();
  test_only_two_answers();
}
