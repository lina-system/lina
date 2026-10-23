// SPDX-License-Identifier: Apache-2.0
// The exchange between the plug-in and the lock broker: one fixed request and
// one fixed reply over a local socket. Fixed sizes keep parsing trivial; the
// nonce binds the reply to this evaluation.
#ifndef LINA_PROTOCOL_H
#define LINA_PROTOCOL_H

#include <stdint.h>

#include "lina_unlock.h"

#define LINA_PROTOCOL_VERSION 1u

#define LINA_REQUEST_MAGIC "LNU1"
#define LINA_REPLY_MAGIC "LNR1"

#define LINA_REQUEST_BYTES 40u
#define LINA_REPLY_BYTES 56u

#define LINA_REPLY_ARMED 0x01u

// request: magic[4] version[1] reserved[3] nonce[32]
#define LINA_REQUEST_NONCE_OFFSET 8u
// reply: magic[4] version[1] flags[1] reserved[2] nonce[32] ticket[16]
#define LINA_REPLY_NONCE_OFFSET 8u
#define LINA_REPLY_TICKET_OFFSET 40u

#endif
