#define _GNU_SOURCE
#include "grant.h"
#include <assert.h>
#include <fcntl.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/stat.h>
#include <sys/wait.h>
#include <unistd.h>

static hu_context context(void) {
    hu_context c = {.uid = 501, .now_ns = 1000000000, .locked = true};
    strcpy(c.session, "11111111-1111-1111-1111-111111111111");
    strcpy(c.boot, "22222222-2222-2222-2222-222222222222");
    return c;
}

int main(void) {
    char directory[] = "/tmp/hermes-unlock-test-XXXXXX";
    assert(mkdtemp(directory));
    uid_t owner = geteuid();
    hu_context c = context(), other;
    const uint64_t ttl = 1000000000;
    char file[256], alias[256];
    snprintf(file, sizeof(file), "%s/grant.bin", directory);
    snprintf(alias, sizeof(alias), "%s/alias", directory);

    assert(hu_consume(directory, owner, &c) == HU_UNAVAILABLE);
    assert(hu_arm(directory, owner, &c, ttl) == HU_OK);
    assert(hu_arm(directory, owner, &c, ttl) == HU_BUSY);
    assert(hu_consume(directory, owner, &c) == HU_OK);
    assert(hu_consumed(directory, owner, &c));
    other = c; other.now_ns++;
    assert(!hu_consumed(directory, owner, &other));
    assert(hu_consume(directory, owner, &c) != HU_OK);
    puts("PASS one-use grant and duplicate-arm rejection");

    assert(hu_arm(directory, owner, &c, ttl) == HU_OK);
    other = c; other.now_ns += ttl;
    assert(hu_consume(directory, owner, &other) == HU_EXPIRED);
    other = c; other.now_ns--;
    assert(hu_consume(directory, owner, &other) == HU_EXPIRED);
    assert(hu_revoke(directory, owner) == HU_OK);
    assert(hu_arm(directory, owner, &c, 0) == HU_INVALID);
    assert(hu_arm(directory, owner, &c, HU_MAX_TTL_NS + 1) == HU_INVALID);
    puts("PASS expiration boundary, clock rollback and TTL bounds");

    assert(hu_arm(directory, owner, &c, ttl) == HU_OK);
    other = c; other.uid++;
    assert(hu_consume(directory, owner, &other) == HU_WRONG_SESSION);
    other = c; other.session[0] = '3';
    assert(hu_consume(directory, owner, &other) == HU_WRONG_SESSION);
    other = c; other.boot[0] = '4';
    assert(hu_consume(directory, owner, &other) == HU_WRONG_SESSION);
    assert(hu_consume(directory, owner, &c) == HU_OK);
    other = c; other.locked = false;
    assert(hu_arm(directory, owner, &other, ttl) == HU_NOT_LOCKED);
    assert(hu_arm(directory, owner, &c, ttl) == HU_OK);
    assert(hu_consume(directory, owner, &other) == HU_NOT_LOCKED);
    assert(hu_revoke(directory, owner) == HU_OK);
    assert(hu_consume(directory, owner, &c) != HU_OK);
    puts("PASS owner, GUI session, boot, lock state and revocation binding");

    assert(chmod(file, 0666) == 0);
    assert(hu_arm(directory, owner, &c, ttl) == HU_UNSAFE_PATH);
    assert(chmod(file, 0600) == 0);
    assert(chmod(directory, 0777) == 0);
    assert(hu_arm(directory, owner, &c, ttl) == HU_UNSAFE_PATH);
    assert(chmod(directory, 0700) == 0);
    assert(link(file, alias) == 0);
    assert(hu_consume(directory, owner, &c) == HU_UNSAFE_PATH);
    assert(unlink(alias) == 0);
    assert(symlink(directory, alias) == 0);
    assert(hu_arm(alias, owner, &c, ttl) == HU_UNSAFE_PATH);
    assert(unlink(alias) == 0);
    assert(hu_arm(directory, owner + 1, &c, ttl) == HU_UNSAFE_PATH);
    puts("PASS writable paths, hard links, symlink directories and wrong file owner rejected");

    int fd = open(file, O_WRONLY | O_TRUNC);
    assert(fd >= 0 && write(fd, "garbage", 7) == 7); close(fd);
    assert(hu_consume(directory, owner, &c) == HU_INVALID);
    assert(hu_revoke(directory, owner) == HU_OK);
    assert(unlink(file) == 0);
    assert(symlink("/dev/null", file) == 0);
    assert(hu_arm(directory, owner, &c, ttl) == HU_UNSAFE_PATH);
    assert(unlink(file) == 0);
    puts("PASS truncated grant and symlink file rejected");

    assert(hu_arm(directory, owner, &c, ttl) == HU_OK);
    int gate[2]; assert(pipe(gate) == 0);
    for (int i = 0; i < 12; i++) {
        pid_t pid = fork(); assert(pid >= 0);
        if (pid == 0) {
            close(gate[1]); char b;
            assert(read(gate[0], &b, 1) == 1);
            _exit(hu_consume(directory, owner, &c) == HU_OK ? 42 : 0);
        }
    }
    close(gate[0]); assert(write(gate[1], "xxxxxxxxxxxx", 12) == 12);
    close(gate[1]); int winners = 0;
    for (int i = 0; i < 12; i++) {
        int status; assert(wait(&status) > 0 && WIFEXITED(status));
        winners += WEXITSTATUS(status) == 42;
    }
    assert(winners == 1);
    puts("PASS twelve competing processes produce exactly one authorization");

    assert(unlink(file) == 0); assert(rmdir(directory) == 0);
    puts("ALL GRANT TESTS PASSED");
    return 0;
}
