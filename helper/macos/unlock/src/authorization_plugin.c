#include "macos_context.h"
#include <Security/AuthorizationPlugin.h>
#include <Security/AuthorizationTags.h>
#include <pwd.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

typedef struct { const AuthorizationCallbacks *callbacks; } plugin;
typedef struct { plugin *plugin; AuthorizationEngineRef engine; } mechanism;

static OSStatus destroy_plugin(AuthorizationPluginRef ref) {
    free(ref); return errAuthorizationSuccess;
}
static OSStatus create_mechanism(AuthorizationPluginRef ref, AuthorizationEngineRef engine,
                                AuthorizationMechanismId name, AuthorizationMechanismRef *out) {
    if (!out) return errAuthorizationInternal;
    *out = NULL;
    if (!ref || !engine || !name || strcmp(name, "authorize")) return errAuthorizationInternal;
    mechanism *m = calloc(1, sizeof(*m));
    if (!m) return errAuthorizationInternal;
    m->plugin = ref; m->engine = engine; *out = m;
    return errAuthorizationSuccess;
}

static bool context_owner_matches(mechanism *m, uid_t uid) {
    const AuthorizationValue *value = NULL;
    AuthorizationContextFlags flags = 0;
    if (m->plugin->callbacks->GetContextValue(m->engine, kAuthorizationEnvironmentUsername,
                                            &flags, &value) ||
        !value || !value->data || !value->length || value->length > 255) return false;
    char name[256];
    memcpy(name, value->data, value->length); name[value->length] = 0;
    /* Accept a single optional trailing NUL, never embedded NUL bytes. */
    size_t length = strnlen(name, value->length);
    if (!length || length + 1 < value->length) return false;
    char buffer[16384]; struct passwd pwd, *result = NULL;
    return getpwnam_r(name, &pwd, buffer, sizeof(buffer), &result) == 0 &&
           result && pwd.pw_uid == uid;
}

static OSStatus invoke_mechanism(AuthorizationMechanismRef ref) {
    if (!ref) return errAuthorizationInternal;
    mechanism *m = ref;
    hu_context context;
    AuthorizationResult result = kAuthorizationResultDeny;
    /* Must be installed as a privileged mechanism. No password, global auth
     * override, GUI event injection or environment-controlled path is used. */
    if (geteuid() == 0 && hu_macos_context(&context) &&
        context_owner_matches(m, context.uid) &&
        hu_consume(HU_DIRECTORY, 0, &context) == HU_OK)
        result = kAuthorizationResultAllow;
    return m->plugin->callbacks->SetResult(m->engine, result);
}
static OSStatus deactivate_mechanism(AuthorizationMechanismRef ref) {
    if (!ref) return errAuthorizationInternal;
    mechanism *m = ref;
    return m->plugin->callbacks->DidDeactivate(m->engine);
}
static OSStatus destroy_mechanism(AuthorizationMechanismRef ref) {
    free(ref); return errAuthorizationSuccess;
}

static const AuthorizationPluginInterface interface = {
    .version = kAuthorizationPluginInterfaceVersion,
    .PluginDestroy = destroy_plugin,
    .MechanismCreate = create_mechanism,
    .MechanismInvoke = invoke_mechanism,
    .MechanismDeactivate = deactivate_mechanism,
    .MechanismDestroy = destroy_mechanism
};

OSStatus AuthorizationPluginCreate(const AuthorizationCallbacks *callbacks,
                                 AuthorizationPluginRef *out,
                                 const AuthorizationPluginInterface **out_interface) {
    if (!out || !out_interface) return errAuthorizationInternal;
    *out = NULL; *out_interface = NULL;
    if (!callbacks || !callbacks->SetResult || !callbacks->DidDeactivate ||
        !callbacks->GetContextValue) return errAuthorizationInternal;
    plugin *p = calloc(1, sizeof(*p));
    if (!p) return errAuthorizationInternal;
    p->callbacks = callbacks; *out = p; *out_interface = &interface;
    return errAuthorizationSuccess;
}
