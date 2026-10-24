// SPDX-License-Identifier: Apache-2.0
// SHA-256 against the published vectors. The backup's seal is the only thing
// standing between a damaged file and the lock screen's rule.
#include <string.h>

#include "sha256.h"
#include "test.h"

static void expect(const char *input, const char *hex) {
  char actual[65];
  const char *inputs[1] = {input};
  size_t lengths[1] = {strlen(input)};
  lina_sha256_hex_inputs(inputs, lengths, 1, actual);
  CHECK_STR(actual, hex);
}

void test_sha256(void) {
  expect("", "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855");
  expect("abc", "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad");
  expect("abcdbcdecdefdefgefghfghighijhijkijkljklmklmnlmnomnopnopq",
         "248d6a61d20638b8e5c026930c3e6039a33ce45964ff2167f6ecedd419db06c1");
  expect("abcdefghbcdefghicdefghijdefghijkefghijklfghijklmghijklmnhijklmnoijklmnopjklmnopqklmnopqrlmnopqrsmnopqrstnopqrstu",
         "cf5b16a778af8380036ce59e7b0492370b249b11e8f07a51afac45037afee9d1");

  // Chunked updates hash the same as one call.
  struct lina_sha256 hash;
  uint8_t digest[LINA_SHA256_BYTES];
  lina_sha256_init(&hash);
  lina_sha256_update(&hash, "ab", 2);
  lina_sha256_update(&hash, "c", 1);
  lina_sha256_final(&hash, digest);
  char hex[65];
  lina_sha256_hex(digest, hex);
  CHECK_STR(hex, "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad");

  // A 64 byte input hashes the same whether it arrives whole or in eight
  // pieces, which exercises the block boundary and the length padding.
  char block[65];
  memset(block, 'a', 64);
  block[64] = '\0';
  struct lina_sha256 first;
  struct lina_sha256 second;
  lina_sha256_init(&first);
  lina_sha256_init(&second);
  lina_sha256_update(&first, block, 64);
  for (int i = 0; i < 8; i++) lina_sha256_update(&second, block + i * 8, 8);
  uint8_t left[LINA_SHA256_BYTES];
  uint8_t right[LINA_SHA256_BYTES];
  lina_sha256_final(&first, left);
  lina_sha256_final(&second, right);
  CHECK_BYTES(left, right, LINA_SHA256_BYTES);

  // Two inputs hash as one string of their concatenation.
  const char *parts[2] = {"block ", "rule"};
  size_t lengths[2] = {6, 4};
  char joined[65];
  lina_sha256_hex_inputs(parts, lengths, 2, joined);
  expect("block rule", joined);
}
