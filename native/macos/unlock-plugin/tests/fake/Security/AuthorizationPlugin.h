// SPDX-License-Identifier: Apache-2.0
// A test stand-in for Apple's <Security/AuthorizationPlugin.h>. It declares
// only the members the plug-in may touch, plus the context, hint and argument
// readers the plug-in must never call. The member names, order and signatures
// follow Apple's header, so a member that this stand-in invents would fail to
// compile against the real SDK. The fake build therefore fails to compile if
// the plug-in reaches for anything else, and the tests install tripwires on the
// forbidden members.
#ifndef LINA_FAKE_AUTHORIZATION_PLUGIN_H
#define LINA_FAKE_AUTHORIZATION_PLUGIN_H

#include <stdint.h>

typedef int32_t OSStatus;
typedef int32_t AuthorizationResult;
typedef uint32_t AuthorizationContextFlags;
typedef uint32_t UInt32;
typedef unsigned long AuthorizationFlags;

typedef struct AuthorizationEngineRefOpaque *AuthorizationEngineRef;
typedef struct AuthorizationPluginRefOpaque *AuthorizationPluginRef;
typedef struct AuthorizationMechanismRefOpaque *AuthorizationMechanismRef;
typedef const char *AuthorizationMechanismId;
typedef const char *AuthorizationString;

// Core Foundation and Security objects the callbacks mention; the plug-in never
// touches them, so the stand-in only needs the names.
typedef const struct __CFType *CFTypeRef;
typedef const struct __CFArray *CFArrayRef;
typedef struct CFDictionary *CFDictionaryRef;

enum {
  kAuthorizationResultAllow = 0,
  kAuthorizationResultDeny = 1,
  kAuthorizationResultUndefined = 2,
  kAuthorizationResultUserCanceled = 3
};

typedef struct AuthorizationValue {
  unsigned long length;
  void *data;
} AuthorizationValue;

typedef struct AuthorizationValueVector {
  unsigned long count;
  char **values;
} AuthorizationValueVector;

typedef struct AuthorizationCallbacks {
  /* Engine callback version. */
  UInt32 version;

  /* Set a result after a call to AuthorizationSessionInvoke. */
  OSStatus (*SetResult)(AuthorizationEngineRef inEngine, AuthorizationResult inResult);

  /* Request authorization engine to interrupt all mechanisms invoked after
     this mechanism has called SessionSetResult. */
  OSStatus (*RequestInterrupt)(AuthorizationEngineRef inEngine);

  /* Respond to the Deactivate request. */
  OSStatus (*DidDeactivate)(AuthorizationEngineRef inEngine);

  /* Read value from context. AuthorizationValue does not own data. */
  OSStatus (*GetContextValue)(AuthorizationEngineRef inEngine, AuthorizationString inKey,
                              AuthorizationContextFlags *outContextFlags,
                              const AuthorizationValue **outValue);

  /* Write value to context. AuthorizationValue and data are copied. */
  OSStatus (*SetContextValue)(AuthorizationEngineRef inEngine, AuthorizationString inKey,
                              AuthorizationContextFlags inContextFlags, const AuthorizationValue *inValue);

  /* Read value from hints. AuthorizationValue does not own data. */
  OSStatus (*GetHintValue)(AuthorizationEngineRef inEngine, AuthorizationString inKey,
                           const AuthorizationValue **outValue);

  /* Write value to hints. AuthorizationValue and data are copied. */
  OSStatus (*SetHintValue)(AuthorizationEngineRef inEngine, AuthorizationString inKey,
                           const AuthorizationValue *inValue);

  /* Read arguments passed. AuthorizationValueVector does not own data. */
  OSStatus (*GetArguments)(AuthorizationEngineRef inEngine, const AuthorizationValueVector **outArguments);

  /* Read SessionId. */
  OSStatus (*GetSessionId)(AuthorizationEngineRef inEngine, void **outSessionId);

  /* Read value from hints. AuthorizationValue does not own data. */
  OSStatus (*GetImmutableHintValue)(AuthorizationEngineRef inEngine, AuthorizationString inKey,
                                    const AuthorizationValue **outValue);

  /* Constructs an LAContext object from the actual user credentials. */
  OSStatus (*GetLAContext)(AuthorizationEngineRef inEngine, CFTypeRef *outValue);

  /* Returns the identities available on tokens. */
  OSStatus (*GetTokenIdentities)(AuthorizationEngineRef inEngine, CFTypeRef context, CFArrayRef *outValue);

  /* Constructs a TKTokenWatcher object. */
  OSStatus (*GetTKTokenWatcher)(AuthorizationEngineRef inEngine, CFTypeRef *outValue);

  /* Remove value from hints. */
  OSStatus (*RemoveHintValue)(AuthorizationEngineRef inEngine, AuthorizationString inKey);

  /* Remove value from context. */
  OSStatus (*RemoveContextValue)(AuthorizationEngineRef inEngine, AuthorizationString inKey);
} AuthorizationCallbacks;

#endif
