#ifndef HU_RUNTIME_H
#define HU_RUNTIME_H
#include "macos_context.h"

#define HU_ROOT "/Library/Application Support/HermesUnlock"
#define HU_AGENT HU_ROOT "/Hermes Unlock Agent.app/Contents/MacOS/hermes-unlock-agent"
#define HU_CTL HU_ROOT "/bin/hermes-unlockctl"
#define HU_CONFIG HU_ROOT "/allowed-uid"
#define HU_WIRE_MAGIC UINT32_C(0x48554732)
enum { HU_GUI_STATUS = 1, HU_GUI_TRIGGER = 2, HU_GUI_PROBE = 3 };
typedef struct {
    uint32_t magic, version, operation, uid;
    char session[HU_UUID_SIZE], boot[HU_UUID_SIZE];
} hu_gui_request;
typedef struct {
    uint32_t magic, version;
    int32_t error, accessibility, displays, legacy_keychain, data_protection_keychain;
} hu_gui_response;

bool hu_allowed_uid(uid_t *uid);
bool hu_prepare_runtime(uid_t uid);
bool hu_gui_path(uid_t uid, char *out, size_t size);
bool hu_same_session(const hu_context *a, const hu_context *b);
bool hu_peer_code_matches(int fd, uid_t uid, const char *executable);
bool hu_exchange(uid_t uid, unsigned operation, const hu_context *context, hu_gui_response *out);
bool hu_transfer(int fd, void *data, size_t length, bool sending);
void hu_socket_timeout(int fd);
int hu_keychain_probe(bool data_protection);
int hu_gui_serve(void);
void hu_gui_handle(const hu_gui_request *request, hu_gui_response *response);
#endif
