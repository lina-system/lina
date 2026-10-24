// SPDX-License-Identifier: Apache-2.0
// A fake authorization database, file system, code check and clock. Every
// installed definition the core writes lands here, so a test can read back what
// macOS would have.
#ifndef LINA_FAKE_HOST_H
#define LINA_FAKE_HOST_H

#include <stddef.h>
#include <stdint.h>

#include "host.h"
#include "rule.h"
#include "state.h"

#define FAKE_RIGHTS 8
#define FAKE_FILES 32
#define FAKE_FILE_BYTES 4096
#define FAKE_WRITES 32
#define FAKE_LOGS 32
#define FAKE_MEASURED 4

struct fake_right {
  bool present;
  char name[64];
  struct lina_right definition;
};

struct fake_file {
  bool present;
  char path[LINA_PATH_MAX];
  char content[FAKE_FILE_BYTES];
  size_t length;
  uint64_t written_ms;
};

struct fake_write {
  char name[64];
  char text[LINA_RULE_TEXT_MAX];
};

struct fake_host {
  struct lina_host host;
  struct fake_right rights[FAKE_RIGHTS];
  struct fake_file files[FAKE_FILES];
  struct fake_write writes[FAKE_WRITES];
  int write_count;
  char logs[FAKE_LOGS][256];
  int log_count;

  // Install conditions.
  bool apple_silicon;
  bool build_known;
  char build[64];
  char measured[FAKE_MEASURED][64];
  int measured_count;
  bool eval_test_mac;

  // Bundle and code. The first bundle is the installed one, or the only one; a
  // copy made while it is present lands in the second, the staged bundle. The
  // text names where a bundle's content came from.
  bool bundle_present;
  char bundle_text[128];
  char bundle_path[LINA_PATH_MAX];
  bool staged_present;
  char staged_text[128];
  char staged_path[LINA_PATH_MAX];
  int verify_result;       // 1 valid, 0 invalid, -1 unreadable
  int copy_status;
  int move_status;         // -1 fails every rename and swap of a bundle
  int guard_install_status;
  int guard_installed;
  int read_right_status;   // -1 forces an unreadable rule
  int write_right_status;  // -1 forces a failing write
  int evaluate_result;     // 1 allow, 0 deny, -1 no answer
  int pending_write_status;
  // A database that accepts the write but normalises it: a read of the lock
  // screen rule then returns the stock rule instead of LINA's.
  bool normalise_rule;
  // The database goes away after the lock screen rule is written.
  bool read_fails_after_write;

  // Clock and session.
  uint64_t now_ms;
  int session_locked_status;  // 0 known, -1 unreadable
  bool locked;
  int build_measured_default;
};

void fake_host_init(struct fake_host *fake);
void fake_right_set(struct fake_host *fake, const char *name, const struct lina_right *definition);
struct fake_right *fake_right_find(struct fake_host *fake, const char *name);
void fake_file_set(struct fake_host *fake, const char *path, const char *content);

#endif
