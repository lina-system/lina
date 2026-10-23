// SPDX-License-Identifier: Apache-2.0
// SHA-256, used to seal the install backup so a damaged or edited file is never
// restored over the lock screen's rule.
#ifndef LINA_SHA256_H
#define LINA_SHA256_H

#include <stddef.h>
#include <stdint.h>

#define LINA_SHA256_BYTES 32

struct lina_sha256 {
  uint32_t state[8];
  uint64_t bits;
  uint8_t block[64];
  size_t used;
};

void lina_sha256_init(struct lina_sha256 *hash);
void lina_sha256_update(struct lina_sha256 *hash, const void *data, size_t len);
void lina_sha256_final(struct lina_sha256 *hash, uint8_t out[LINA_SHA256_BYTES]);
void lina_sha256_hex(const uint8_t digest[LINA_SHA256_BYTES], char out[65]);
void lina_sha256_hex_inputs(const char *const *inputs, const size_t *lengths, size_t count, char out[65]);

#endif
