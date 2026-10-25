#include <Security/AuthorizationPlugin.h>
#include <assert.h>
#include <dlfcn.h>
#include <stdio.h>
#include <unistd.h>

static int results = 0, deactivations = 0;
static OSStatus set_result(AuthorizationEngineRef engine, AuthorizationResult result) {
    (void)engine; assert(result == kAuthorizationResultDeny); results++; return 0;
}
static OSStatus deactivated(AuthorizationEngineRef engine) {
    (void)engine; deactivations++; return 0;
}
static OSStatus get_context(AuthorizationEngineRef e, AuthorizationString key,
                           AuthorizationContextFlags *flags, const AuthorizationValue **value) {
    (void)e; (void)key; (void)flags; *value = NULL; return errAuthorizationDenied;
}

int main(int argc, char **argv) {
    assert(argc == 2 && geteuid() != 0);
    void *handle = dlopen(argv[1], RTLD_NOW | RTLD_LOCAL);
    if (!handle) { fprintf(stderr, "%s\n", dlerror()); return 1; }
    OSStatus (*create)(const AuthorizationCallbacks *, AuthorizationPluginRef *,
                       const AuthorizationPluginInterface **) = dlsym(handle, "AuthorizationPluginCreate");
    assert(create);
    AuthorizationCallbacks callbacks = {.version = kAuthorizationCallbacksVersion,
        .SetResult = set_result, .DidDeactivate = deactivated, .GetContextValue = get_context};
    AuthorizationPluginRef plugin = NULL;
    const AuthorizationPluginInterface *api = NULL;
    assert(create(NULL, &plugin, &api) != 0);
    assert(create(&callbacks, &plugin, &api) == 0);
    AuthorizationMechanismRef m = NULL;
    AuthorizationEngineRef engine = (AuthorizationEngineRef)&callbacks;
    assert(api->MechanismCreate(plugin, engine, "wrong-mechanism", &m) != 0 && !m);
    assert(api->MechanismCreate(plugin, engine, "authorize", &m) == 0);
    assert(api->MechanismInvoke(m) == 0 && results == 1);
    assert(api->MechanismDeactivate(m) == 0 && deactivations == 1);
    assert(api->MechanismDestroy(m) == 0);
    assert(api->PluginDestroy(plugin) == 0);
    dlclose(handle);
    puts("PASS native bundle load, invalid callbacks/mechanism, deny and deactivate callbacks");
    puts("NOTE this is an unprivileged test host, not SecurityAgentHelper");
}
