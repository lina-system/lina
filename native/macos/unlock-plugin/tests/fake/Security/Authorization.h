// SPDX-License-Identifier: Apache-2.0
// A test stand-in for the status codes the plug-in returns. The Linux build
// has no Security.framework; the values match Apple's Authorization.h.
#ifndef LINA_FAKE_AUTHORIZATION_H
#define LINA_FAKE_AUTHORIZATION_H

#include <stdint.h>

typedef int32_t OSStatus;

enum {
  errAuthorizationSuccess = 0,
  errAuthorizationInternal = -60008
};

#endif
