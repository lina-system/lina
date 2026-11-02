#ifndef HERMES_GRANT_H
#define HERMES_GRANT_H
#include <stdbool.h>
#include <stdint.h>
#include <sys/types.h>

#define HU_UUID_SIZE 37
#define HU_DIRECTORY "/var/run/hermes-unlock"
#define HU_MAX_TTL_NS UINT64_C(10000000000)

typedef struct {
    uid_t uid;
    char session[HU_UUID_SIZE];
    char boot[HU_UUID_SIZE];
    uint64_t now_ns;
    bool locked;
} hu_context;

typedef enum {
    HU_OK = 0, HU_UNAVAILABLE, HU_UNSAFE_PATH, HU_INVALID,
    HU_NOT_LOCKED, HU_BUSY, HU_EXPIRED, HU_WRONG_SESSION
} hu_result;

/* Caller supplies trusted OS facts, never model/client-supplied context.
 * Production callers always use HU_DIRECTORY and owner=0.
 * All mutations lock the same persistent file. Consuming marks used and fsyncs
 * before returning HU_OK. Failure never grants authorization. */
hu_result hu_arm(const char *directory, uid_t owner,
                 const hu_context *context, uint64_t ttl_ns);
hu_result hu_consume(const char *directory, uid_t owner,
                     const hu_context *context);
hu_result hu_revoke(const char *directory, uid_t owner);
bool hu_consumed(const char *directory, uid_t owner, const hu_context *issued);
const char *hu_result_name(hu_result result);
#endif
