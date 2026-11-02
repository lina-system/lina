#define _GNU_SOURCE
#include "grant.h"
#include <errno.h>
#include <fcntl.h>
#include <string.h>
#include <sys/file.h>
#include <sys/stat.h>
#include <unistd.h>

typedef struct {
    uint64_t magic, version, issued_ns, expires_ns, consumed;
    uint64_t uid;
    char session[HU_UUID_SIZE], boot[HU_UUID_SIZE];
} grant;
static const uint64_t MAGIC = UINT64_C(0x48554e4c4f434b31);

static bool uuid_valid(const char *s) {
    for (int i = 0; i < 36; i++) {
        if (i == 8 || i == 13 || i == 18 || i == 23) {
            if (s[i] != '-') return false;
        } else if (!((s[i] >= '0' && s[i] <= '9') ||
                     (s[i] >= 'a' && s[i] <= 'f') ||
                     (s[i] >= 'A' && s[i] <= 'F'))) return false;
    }
    return s[36] == 0;
}
static bool context_valid(const hu_context *c) {
    return c && c->uid != 0 && c->uid != (uid_t)-1 &&
           uuid_valid(c->session) && uuid_valid(c->boot);
}

static hu_result open_grant(const char *directory, uid_t owner, bool create, int *out) {
    *out = -1;
    int dir = open(directory, O_RDONLY | O_DIRECTORY | O_NOFOLLOW | O_CLOEXEC);
    if (dir < 0) return errno == ENOENT ? HU_UNAVAILABLE : HU_UNSAFE_PATH;
    struct stat st;
    if (fstat(dir, &st) || st.st_uid != owner || (st.st_mode & 0022)) {
        close(dir); return HU_UNSAFE_PATH;
    }
    int fd = openat(dir, "grant.bin", O_RDWR | O_NOFOLLOW | O_CLOEXEC |
                    (create ? O_CREAT : 0), 0600);
    int saved_errno = errno;
    close(dir);
    if (fd < 0) return saved_errno == ENOENT ? HU_UNAVAILABLE : HU_UNSAFE_PATH;
    if (fstat(fd, &st) || !S_ISREG(st.st_mode) || st.st_uid != owner ||
        (st.st_mode & 0777) != 0600 || st.st_nlink != 1) {
        close(fd); return HU_UNSAFE_PATH;
    }
    /* Never wait on a stuck competing process inside the authentication host. */
    if (flock(fd, LOCK_EX | LOCK_NB)) { close(fd); return HU_BUSY; }
    *out = fd;
    return HU_OK;
}

static hu_result read_grant(int fd, grant *g) {
    struct stat st;
    if (fstat(fd, &st)) return HU_UNAVAILABLE;
    if (st.st_size == 0) return HU_UNAVAILABLE;
    if (st.st_size != sizeof(*g) || pread(fd, g, sizeof(*g), 0) != sizeof(*g))
        return HU_INVALID;
    if (g->magic != MAGIC || g->version != 2 || g->consumed > 1 || !uuid_valid(g->session) ||
        !uuid_valid(g->boot) || g->expires_ns <= g->issued_ns ||
        g->expires_ns - g->issued_ns > HU_MAX_TTL_NS) return HU_INVALID;
    return HU_OK;
}
static hu_result clear_grant(int fd) {
    return ftruncate(fd, 0) == 0 && fsync(fd) == 0 ? HU_OK : HU_UNAVAILABLE;
}
static bool binding_matches(const grant *g, const hu_context *c) {
    return g->uid == c->uid && strcmp(g->session, c->session) == 0 &&
           strcmp(g->boot, c->boot) == 0;
}

hu_result hu_arm(const char *d, uid_t owner, const hu_context *c, uint64_t ttl) {
    if (!context_valid(c) || !ttl || ttl > HU_MAX_TTL_NS ||
        c->now_ns > UINT64_MAX - ttl) return HU_INVALID;
    if (!c->locked) return HU_NOT_LOCKED;
    int fd;
    hu_result r = open_grant(d, owner, true, &fd);
    if (r != HU_OK) return r;
    grant old;
    r = read_grant(fd, &old);
    if (r == HU_INVALID) { close(fd); return r; }
    if (r == HU_OK && !old.consumed && strcmp(old.boot, c->boot) == 0 &&
        old.issued_ns <= c->now_ns && c->now_ns < old.expires_ns) {
        close(fd); return HU_BUSY;
    }
    grant g = {0};
    g.magic = MAGIC; g.version = 2; g.uid = c->uid;
    g.issued_ns = c->now_ns; g.expires_ns = c->now_ns + ttl;
    memcpy(g.session, c->session, sizeof(g.session));
    memcpy(g.boot, c->boot, sizeof(g.boot));
    r = clear_grant(fd);
    if (r == HU_OK && (pwrite(fd, &g, sizeof(g), 0) != sizeof(g) || fsync(fd)))
        r = HU_UNAVAILABLE;
    close(fd);
    return r;
}

hu_result hu_consume(const char *d, uid_t owner, const hu_context *c) {
    if (!context_valid(c)) return HU_INVALID;
    if (!c->locked) return HU_NOT_LOCKED;
    int fd;
    hu_result r = open_grant(d, owner, false, &fd);
    if (r != HU_OK) return r;
    grant g;
    r = read_grant(fd, &g);
    if (r == HU_OK && g.consumed) r = HU_UNAVAILABLE;
    if (r == HU_OK && !binding_matches(&g, c)) r = HU_WRONG_SESSION;
    if (r == HU_OK && (c->now_ns < g.issued_ns || c->now_ns >= g.expires_ns))
        r = HU_EXPIRED;
    if (r == HU_OK) {
        g.consumed = 1;
        if (pwrite(fd, &g, sizeof(g), 0) != sizeof(g) || fsync(fd)) r = HU_UNAVAILABLE;
    }
    close(fd);
    return r;
}

hu_result hu_revoke(const char *d, uid_t owner) {
    int fd;
    hu_result r = open_grant(d, owner, false, &fd);
    if (r != HU_OK) return r;
    r = clear_grant(fd); close(fd); return r;
}

bool hu_consumed(const char *d, uid_t owner, const hu_context *c) {
    if (!context_valid(c)) return false;
    int fd;
    if (open_grant(d, owner, false, &fd) != HU_OK) return false;
    grant g;
    bool result = read_grant(fd, &g) == HU_OK && g.consumed == 1 &&
                  binding_matches(&g, c) && g.issued_ns == c->now_ns;
    close(fd); return result;
}

const char *hu_result_name(hu_result r) {
    switch (r) {
    case HU_OK: return "ok";
    case HU_UNAVAILABLE: return "unavailable";
    case HU_UNSAFE_PATH: return "unsafe_path";
    case HU_INVALID: return "invalid";
    case HU_NOT_LOCKED: return "not_locked";
    case HU_BUSY: return "busy";
    case HU_EXPIRED: return "expired";
    case HU_WRONG_SESSION: return "wrong_session";
    }
    return "invalid";
}
