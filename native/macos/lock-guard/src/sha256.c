// SPDX-License-Identifier: Apache-2.0
#include "sha256.h"

#include <string.h>

static const uint32_t k[64] = {
  0x428a2f98u, 0x71374491u, 0xb5c0fbcfu, 0xe9b5dba5u, 0x3956c25bu, 0x59f111f1u, 0x923f82a4u, 0xab1c5ed5u,
  0xd807aa98u, 0x12835b01u, 0x243185beu, 0x550c7dc3u, 0x72be5d74u, 0x80deb1feu, 0x9bdc06a7u, 0xc19bf174u,
  0xe49b69c1u, 0xefbe4786u, 0x0fc19dc6u, 0x240ca1ccu, 0x2de92c6fu, 0x4a7484aau, 0x5cb0a9dcu, 0x76f988dau,
  0x983e5152u, 0xa831c66du, 0xb00327c8u, 0xbf597fc7u, 0xc6e00bf3u, 0xd5a79147u, 0x06ca6351u, 0x14292967u,
  0x27b70a85u, 0x2e1b2138u, 0x4d2c6dfcu, 0x53380d13u, 0x650a7354u, 0x766a0abbu, 0x81c2c92eu, 0x92722c85u,
  0xa2bfe8a1u, 0xa81a664bu, 0xc24b8b70u, 0xc76c51a3u, 0xd192e819u, 0xd6990624u, 0xf40e3585u, 0x106aa070u,
  0x19a4c116u, 0x1e376c08u, 0x2748774cu, 0x34b0bcb5u, 0x391c0cb3u, 0x4ed8aa4au, 0x5b9cca4fu, 0x682e6ff3u,
  0x748f82eeu, 0x78a5636fu, 0x84c87814u, 0x8cc70208u, 0x90befffau, 0xa4506cebu, 0xbef9a3f7u, 0xc67178f2u,
};

static uint32_t rotr(uint32_t value, unsigned bits) { return (value >> bits) | (value << (32u - bits)); }

static void compress(struct lina_sha256 *hash, const uint8_t block[64]) {
  uint32_t words[64];
  for (int i = 0; i < 16; i++) {
    words[i] = (uint32_t)block[i * 4] << 24 | (uint32_t)block[i * 4 + 1] << 16
             | (uint32_t)block[i * 4 + 2] << 8 | (uint32_t)block[i * 4 + 3];
  }
  for (int i = 16; i < 64; i++) {
    uint32_t s0 = rotr(words[i - 15], 7) ^ rotr(words[i - 15], 18) ^ (words[i - 15] >> 3);
    uint32_t s1 = rotr(words[i - 2], 17) ^ rotr(words[i - 2], 19) ^ (words[i - 2] >> 10);
    words[i] = words[i - 16] + s0 + words[i - 7] + s1;
  }
  uint32_t a = hash->state[0], b = hash->state[1], c = hash->state[2], d = hash->state[3];
  uint32_t e = hash->state[4], f = hash->state[5], g = hash->state[6], h = hash->state[7];
  for (int i = 0; i < 64; i++) {
    uint32_t s1 = rotr(e, 6) ^ rotr(e, 11) ^ rotr(e, 25);
    uint32_t choice = (e & f) ^ (~e & g);
    uint32_t t1 = h + s1 + choice + k[i] + words[i];
    uint32_t s0 = rotr(a, 2) ^ rotr(a, 13) ^ rotr(a, 22);
    uint32_t majority = (a & b) ^ (a & c) ^ (b & c);
    uint32_t t2 = s0 + majority;
    h = g;
    g = f;
    f = e;
    e = d + t1;
    d = c;
    c = b;
    b = a;
    a = t1 + t2;
  }
  hash->state[0] += a;
  hash->state[1] += b;
  hash->state[2] += c;
  hash->state[3] += d;
  hash->state[4] += e;
  hash->state[5] += f;
  hash->state[6] += g;
  hash->state[7] += h;
}

void lina_sha256_init(struct lina_sha256 *hash) {
  static const uint32_t initial[8] = {
    0x6a09e667u, 0xbb67ae85u, 0x3c6ef372u, 0xa54ff53au,
    0x510e527fu, 0x9b05688cu, 0x1f83d9abu, 0x5be0cd19u,
  };
  memcpy(hash->state, initial, sizeof(initial));
  hash->bits = 0;
  hash->used = 0;
}

void lina_sha256_update(struct lina_sha256 *hash, const void *data, size_t len) {
  const uint8_t *bytes = data;
  hash->bits += (uint64_t)len * 8u;
  while (len > 0) {
    size_t take = 64 - hash->used;
    if (take > len) take = len;
    memcpy(hash->block + hash->used, bytes, take);
    hash->used += take;
    bytes += take;
    len -= take;
    if (hash->used == 64) {
      compress(hash, hash->block);
      hash->used = 0;
    }
  }
}

void lina_sha256_final(struct lina_sha256 *hash, uint8_t out[LINA_SHA256_BYTES]) {
  uint64_t bits = hash->bits;
  size_t used = hash->used;
  hash->block[used++] = 0x80u;
  if (used > 56) {
    memset(hash->block + used, 0, 64 - used);
    compress(hash, hash->block);
    used = 0;
  }
  memset(hash->block + used, 0, 56 - used);
  for (int i = 0; i < 8; i++) hash->block[56 + i] = (uint8_t)(bits >> (56 - i * 8));
  compress(hash, hash->block);
  hash->used = 0;
  for (int i = 0; i < 8; i++) {
    out[i * 4] = (uint8_t)(hash->state[i] >> 24);
    out[i * 4 + 1] = (uint8_t)(hash->state[i] >> 16);
    out[i * 4 + 2] = (uint8_t)(hash->state[i] >> 8);
    out[i * 4 + 3] = (uint8_t)hash->state[i];
  }
}

void lina_sha256_hex(const uint8_t digest[LINA_SHA256_BYTES], char out[65]) {
  static const char digits[] = "0123456789abcdef";
  for (int i = 0; i < LINA_SHA256_BYTES; i++) {
    out[i * 2] = digits[digest[i] >> 4];
    out[i * 2 + 1] = digits[digest[i] & 0x0fu];
  }
  out[64] = '\0';
}

void lina_sha256_hex_inputs(const char *const *inputs, const size_t *lengths, size_t count, char out[65]) {
  struct lina_sha256 hash;
  uint8_t digest[LINA_SHA256_BYTES];
  lina_sha256_init(&hash);
  for (size_t i = 0; i < count; i++) lina_sha256_update(&hash, inputs[i], lengths[i]);
  lina_sha256_final(&hash, digest);
  lina_sha256_hex(digest, out);
}
