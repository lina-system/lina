#include "macos_context.h"
#include <Security/AuthorizationPlugin.h>
#include <assert.h>
#include <pwd.h>
#include <stdio.h>
#include <string.h>
#include <unistd.h>

/* Only this separate test executable replaces OS facts. The production bundle
 * is compiled without these functions or the test-only preprocessor define. */
static uid_t test_euid;
static bool have_context = true, have_username = true;
static hu_context snapshot;
static hu_result pending = HU_OK;
static int consumes, notifications;
static AuthorizationResult result;
static char username[256];

uid_t hu_test_euid(void) { return test_euid; }
bool hu_macos_context(hu_context *out) { *out = snapshot; return have_context; }
hu_result hu_consume(const char *directory, uid_t owner, const hu_context *c) {
    assert(strcmp(directory, HU_DIRECTORY) == 0 && owner == 0 && c->uid == snapshot.uid);
    consumes++; hu_result r = pending; pending = HU_UNAVAILABLE; return r;
}
static OSStatus set_result(AuthorizationEngineRef e, AuthorizationResult r) {
    (void)e; notifications++; result = r; return 0;
}
static OSStatus deactivated(AuthorizationEngineRef e) { (void)e; return 0; }
static OSStatus get_context(AuthorizationEngineRef e, AuthorizationString key,
                           AuthorizationContextFlags *flags, const AuthorizationValue **out) {
    (void)e; (void)key; (void)flags;
    static AuthorizationValue value;
    value.data = username; value.length = strlen(username);
    *out = have_username ? &value : NULL;
    return have_username ? 0 : errAuthorizationDenied;
}
int main(void) {
    struct passwd *user = getpwuid(getuid()); assert(user);
    snprintf(username, sizeof(username), "%s", user->pw_name);
    snapshot.uid = user->pw_uid;
    AuthorizationCallbacks callbacks = {.version = kAuthorizationCallbacksVersion,
        .SetResult = set_result, .DidDeactivate = deactivated, .GetContextValue = get_context};
    AuthorizationPluginRef plugin;
    const AuthorizationPluginInterface *api;
    assert(AuthorizationPluginCreate(&callbacks, &plugin, &api) == 0);
    AuthorizationMechanismRef m;
    assert(api->MechanismCreate(plugin, (AuthorizationEngineRef)&callbacks, "authorize", &m) == 0);

    assert(api->MechanismInvoke(m) == 0 && result == kAuthorizationResultAllow && consumes == 1);
    assert(api->MechanismInvoke(m) == 0 && result == kAuthorizationResultDeny && consumes == 2);
    puts("PASS authorization maps one-use grant to Allow and replay to Deny");

    pending = HU_EXPIRED;
    assert(api->MechanismInvoke(m) == 0 && result == kAuthorizationResultDeny && consumes == 3);
    have_username = false;
    assert(api->MechanismInvoke(m) == 0 && result == kAuthorizationResultDeny && consumes == 3);
    have_username = true; snapshot.uid++;
    assert(api->MechanismInvoke(m) == 0 && result == kAuthorizationResultDeny && consumes == 3);
    snapshot.uid--; have_context = false;
    assert(api->MechanismInvoke(m) == 0 && result == kAuthorizationResultDeny && consumes == 3);
    have_context = true; test_euid = 501;
    assert(api->MechanismInvoke(m) == 0 && result == kAuthorizationResultDeny && consumes == 3);
    assert(notifications == 7);
    puts("PASS expired grant, missing username, different owner, missing console and unprivileged host deny");
    api->MechanismDestroy(m); api->PluginDestroy(plugin);
    puts("NOTE positive authorization uses synthetic OS facts in a separate test executable");
}
