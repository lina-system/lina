#include "runtime.h"
#include <Security/Security.h>
#include <errno.h>
#include <fcntl.h>
#include <mach/message.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/socket.h>
#include <sys/stat.h>
#include <sys/un.h>
#include <unistd.h>

bool hu_allowed_uid(uid_t *uid) {
    int fd = open(HU_CONFIG, O_RDONLY | O_NOFOLLOW | O_CLOEXEC);
    if (fd < 0) return false;
    struct stat st; char buffer[32] = {0};
    bool ok = fstat(fd, &st) == 0 && S_ISREG(st.st_mode) && st.st_uid == 0 &&
              st.st_nlink == 1 && !(st.st_mode & 0022) && st.st_size > 0 && st.st_size < 20;
    ssize_t n = ok ? read(fd, buffer, sizeof(buffer)-1) : -1;
    close(fd);
    if (n <= 0) return false;
    char *end; errno = 0;
    unsigned long value = strtoul(buffer, &end, 10);
    if (errno || end == buffer || (*end && strcmp(end, "\n")) || value < 501 || value >= UINT32_MAX)
        return false;
    *uid = (uid_t)value; return true;
}

static bool directory(const char *path, uid_t uid, mode_t mode) {
    bool made = mkdir(path, mode) == 0;
    if (!made && errno != EEXIST) return false;
    int fd = open(path, O_RDONLY | O_DIRECTORY | O_NOFOLLOW | O_CLOEXEC);
    if (fd < 0) return false;
    struct stat st;
    bool ok = fstat(fd, &st) == 0 && S_ISDIR(st.st_mode);
    if (ok && made) ok = fchown(fd, uid, 0) == 0 && fchmod(fd, mode) == 0;
    if (ok) ok = fstat(fd, &st) == 0 && st.st_uid == uid && (st.st_mode & 0777) == mode;
    close(fd); return ok;
}
bool hu_prepare_runtime(uid_t uid) {
    if (geteuid() != 0 || !directory(HU_DIRECTORY, 0, 0755)) return false;
    char path[128];
    snprintf(path, sizeof(path), HU_DIRECTORY "/gui-%u", uid);
    return directory(path, uid, 0700);
}
bool hu_gui_path(uid_t uid, char *out, size_t size) {
    int n = snprintf(out, size, HU_DIRECTORY "/gui-%u/agent.sock", uid);
    return n > 0 && (size_t)n < size;
}
bool hu_same_session(const hu_context *a, const hu_context *b) {
    return a->uid == b->uid && strcmp(a->session, b->session) == 0 && strcmp(a->boot, b->boot) == 0;
}
void hu_socket_timeout(int fd) {
    struct timeval t = {.tv_sec = 3, .tv_usec = 0};
    int one = 1;
    setsockopt(fd, SOL_SOCKET, SO_RCVTIMEO, &t, sizeof(t));
    setsockopt(fd, SOL_SOCKET, SO_SNDTIMEO, &t, sizeof(t));
    setsockopt(fd, SOL_SOCKET, SO_NOSIGPIPE, &one, sizeof(one));
}
bool hu_transfer(int fd, void *data, size_t length, bool sending) {
    size_t offset = 0;
    while (offset < length) {
        ssize_t n = sending ? send(fd, (char *)data + offset, length-offset, 0)
                            : recv(fd, (char *)data + offset, length-offset, 0);
        if (n < 0 && errno == EINTR) continue;
        if (n <= 0) return false;
        offset += (size_t)n;
    }
    return true;
}

bool hu_peer_code_matches(int fd, uid_t uid, const char *executable) {
    uid_t peer_uid; gid_t peer_gid;
    if (getpeereid(fd, &peer_uid, &peer_gid) || peer_uid != uid) return false;
    audit_token_t token; socklen_t size = sizeof(token);
    if (getsockopt(fd, SOL_LOCAL, LOCAL_PEERTOKEN, &token, &size) || size != sizeof(token)) return false;
    CFDataRef audit = CFDataCreate(NULL, (const UInt8 *)&token, sizeof(token));
    const void *keys[] = {kSecGuestAttributeAudit}, *values[] = {audit};
    CFDictionaryRef attrs = CFDictionaryCreate(NULL, keys, values, 1,
        &kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
    SecCodeRef peer = NULL; SecStaticCodeRef installed = NULL;
    CFDictionaryRef peer_info = NULL, installed_info = NULL;
    CFURLRef url = CFURLCreateFromFileSystemRepresentation(NULL, (const UInt8 *)executable, strlen(executable), false);
    bool ok = audit && attrs && url &&
        SecCodeCopyGuestWithAttributes(NULL, attrs, kSecCSDefaultFlags, &peer) == errSecSuccess &&
        SecCodeCheckValidity(peer, kSecCSStrictValidate, NULL) == errSecSuccess &&
        SecStaticCodeCreateWithPath(url, kSecCSDefaultFlags, &installed) == errSecSuccess &&
        SecStaticCodeCheckValidity(installed, kSecCSStrictValidate, NULL) == errSecSuccess &&
        SecCodeCopySigningInformation(peer, kSecCSSigningInformation, &peer_info) == errSecSuccess &&
        SecCodeCopySigningInformation((SecCodeRef)installed, kSecCSSigningInformation, &installed_info) == errSecSuccess;
    if (ok) {
        CFTypeRef a = CFDictionaryGetValue(peer_info, kSecCodeInfoUnique);
        CFTypeRef b = CFDictionaryGetValue(installed_info, kSecCodeInfoUnique);
        ok = a && b && CFEqual(a,b);
    }
    if (installed_info) CFRelease(installed_info);
    if (peer_info) CFRelease(peer_info);
    if (installed) CFRelease(installed);
    if (peer) CFRelease(peer);
    if (url) CFRelease(url);
    if (attrs) CFRelease(attrs);
    if (audit) CFRelease(audit);
    return ok;
}

bool hu_exchange(uid_t uid, unsigned op, const hu_context *context, hu_gui_response *out) {
    memset(out, 0, sizeof(*out));
    struct sockaddr_un address = {.sun_family = AF_UNIX};
    if (!hu_gui_path(uid, address.sun_path, sizeof(address.sun_path))) return false;
    int fd = socket(AF_UNIX, SOCK_STREAM, 0);
    if (fd < 0) return false;
    hu_socket_timeout(fd);
    hu_gui_request request = {.magic = HU_WIRE_MAGIC, .version = 1, .operation = op, .uid = uid};
    if (context) {
        memcpy(request.session, context->session, sizeof(request.session));
        memcpy(request.boot, context->boot, sizeof(request.boot));
    }
    bool ok = connect(fd, (struct sockaddr *)&address, sizeof(address)) == 0 &&
        hu_peer_code_matches(fd, uid, HU_AGENT) &&
        hu_transfer(fd, &request, sizeof(request), true) &&
        hu_transfer(fd, out, sizeof(*out), false) &&
        out->magic == HU_WIRE_MAGIC && out->version == 1;
    close(fd); return ok;
}
