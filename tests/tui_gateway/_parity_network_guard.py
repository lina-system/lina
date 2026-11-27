"""Confine libc connect calls in the Linux parity subprocesses to loopback."""

import os
import subprocess
from collections.abc import Iterator
from contextlib import contextmanager
from pathlib import Path
from typing import Final

CONNECT_GUARD: Final = r"""
#define _GNU_SOURCE
#include <arpa/inet.h>
#include <dlfcn.h>
#include <errno.h>
#include <fcntl.h>
#include <stddef.h>
#include <stdio.h>
#include <stdlib.h>
#include <unistd.h>

int connect(int fd, const struct sockaddr *addr, socklen_t len) {
    char ip[INET6_ADDRSTRLEN];
    char destination[INET6_ADDRSTRLEN + 16];
    if (addr && len >= sizeof(struct sockaddr_in) && addr->sa_family == AF_INET) {
        const struct sockaddr_in *v4 = (const struct sockaddr_in *)addr;
        if ((ntohl(v4->sin_addr.s_addr) >> 24) == 127)
            goto allowed;
        inet_ntop(AF_INET, &v4->sin_addr, ip, sizeof(ip));
        snprintf(destination, sizeof(destination), "%s:%u\n", ip, ntohs(v4->sin_port));
    } else if (addr && len >= offsetof(struct sockaddr_in6, sin6_scope_id) && addr->sa_family == AF_INET6) {
        const struct sockaddr_in6 *v6 = (const struct sockaddr_in6 *)addr;
        if (IN6_IS_ADDR_LOOPBACK(&v6->sin6_addr) ||
            (IN6_IS_ADDR_V4MAPPED(&v6->sin6_addr) && v6->sin6_addr.s6_addr[12] == 127))
            goto allowed;
        inet_ntop(AF_INET6, &v6->sin6_addr, ip, sizeof(ip));
        snprintf(destination, sizeof(destination), "[%s]:%u\n", ip, ntohs(v6->sin6_port));
    } else {
        goto allowed;
    }
    int log = open(getenv("PARITY_CONNECT_LOG"), O_WRONLY | O_APPEND | O_CLOEXEC);
    if (log < 0 || dprintf(log, "%s", destination) < 0 || close(log) < 0) {
        dprintf(STDERR_FILENO, "Cannot record forbidden parity connection to %s", destination);
        _exit(91);
    }
    errno = EACCES;
    return -1;

allowed:
    return ((int (*)(int, const struct sockaddr *, socklen_t))dlsym(RTLD_NEXT, "connect"))(fd, addr, len);
}
"""


def build_network_guard(root: Path) -> Path:
    library = root / "connect-guard.so"
    subprocess.run(
        ["cc", "-x", "c", "-shared", "-fPIC", "-o", str(library), "-", "-ldl"],
        input=CONNECT_GUARD, text=True, check=True, env={"PATH": os.defpath, "TMPDIR": str(root)},
    )
    return library


@contextmanager
def guarded_arm(library: Path, home: Path, extra_env: dict[str, str]) -> Iterator[dict[str, str]]:
    log = home / "connect-attempts"
    log.touch(exist_ok=False)
    preload = ":".join(filter(None, (str(library), extra_env.get("LD_PRELOAD"))))
    try:
        yield {**extra_env, "LD_PRELOAD": preload, "PARITY_CONNECT_LOG": str(log)}
    finally:
        attempts = log.read_text(encoding="ascii")
        assert not attempts, f"Forbidden parity connection:\n{attempts}"
