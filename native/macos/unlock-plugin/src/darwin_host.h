// SPDX-License-Identifier: Apache-2.0
// The macOS side of the unlock plug-in: the files the installer writes, the
// broker socket, the peer's audit token and code signature, the monotonic clock
// and the random source. Everything here sits behind `struct lina_host`.
#ifndef LINA_DARWIN_HOST_H
#define LINA_DARWIN_HOST_H

#include "lina_unlock.h"

const struct lina_host *lina_darwin_host(void);

#endif
