// SPDX-License-Identifier: Apache-2.0
// The rollback command: root restores directly, the person asks the guard
// through one fixed frame, and a malformed frame is refused untouched.
#include <string.h>

#include "guard.h"
#include "install.h"
#include "restore.h"
#include "rule.h"
#include "state.h"
#include "test.h"
#include "fake_host.h"

static struct lina_paths paths;

static void installed(struct fake_host *fake) {
  fake_host_init(fake);
  lina_paths_prefix(&paths, "/tmp/lina-test");
  struct lina_install_input input = {
      .bundle_source = "/release/LinaUnlock.bundle",
      .broker_socket = "/var/run/lina/broker.sock",
      .person_uid = 501,
      .deadline_ms = 500,
  };
  CHECK(lina_install(&fake->host, &paths, &input).code == LINA_INSTALL_OK);
  fake->write_count = 0;
}

// Plays the guard's side of the socket: the person's account sends the frame,
// a second host that stands for the guard answers it.
struct fake_socket {
  struct fake_host *guard;
  bool drop;
  bool corrupt;
  int calls;
};

static int send_request(void *context, const uint8_t request[LINA_RESTORE_FRAME],
                        uint8_t reply[LINA_RESTORE_FRAME], bool *refused) {
  struct fake_socket *socket = context;
  socket->calls++;
  if (socket->drop) {
    *refused = false;
    return -1;
  }
  lina_restore_handle(&socket->guard->host, &paths, request, reply);
  if (socket->corrupt) reply[7] = 0xff;  // trailing bytes are not tolerated
  return 0;
}

void test_restore(void) {
  struct lina_right stock;
  lina_rule_stock(&stock);

  // The frames are fixed and self-describing.
  uint8_t request[LINA_RESTORE_FRAME];
  uint8_t reply[LINA_RESTORE_FRAME];
  lina_restore_request_encode(request);
  CHECK(lina_restore_request_decode(request) == 0);
  request[6] = 0x02;  // no other operation exists
  CHECK(lina_restore_request_decode(request) != 0);
  lina_restore_request_encode(request);
  request[LINA_RESTORE_FRAME - 1] = 1;
  CHECK(lina_restore_request_decode(request) != 0);
  memset(request, 0, LINA_RESTORE_FRAME);
  CHECK(lina_restore_request_decode(request) != 0);

  lina_restore_reply_encode(LINA_RESTORE_OK, reply);
  enum lina_restore_status status;
  CHECK(lina_restore_reply_decode(reply, &status) == 0);
  CHECK(status == LINA_RESTORE_OK);
  reply[6] = 0x7f;
  CHECK(lina_restore_reply_decode(reply, &status) != 0);

  // Root: the restore runs in place.
  struct fake_host root;
  installed(&root);
  CHECK(lina_rollback_run(&root.host, &paths, true, NULL) == LINA_ROLLBACK_RESTORED);
  struct fake_right *slot = fake_right_find(&root, LINA_RIGHT_SCREENSAVER);
  CHECK(slot != NULL && lina_right_equal_ignoring_timestamps(&slot->definition, &stock));
  CHECK(!lina_enabled_present(&root.host, &paths));

  // The person's account: one request over the guard's socket, no password.
  struct fake_host guard;
  installed(&guard);
  struct fake_socket socket = {&guard, false, false, 0};
  struct lina_rollback transport = {&socket, send_request};
  CHECK(lina_rollback_run(&guard.host, &paths, false, &transport) == LINA_ROLLBACK_RESTORED);
  CHECK(socket.calls == 1);
  slot = fake_right_find(&guard, LINA_RIGHT_SCREENSAVER);
  CHECK(slot != NULL && lina_right_equal_ignoring_timestamps(&slot->definition, &stock));
  CHECK(!lina_enabled_present(&guard.host, &paths));

  // No transport and no root: nothing happens and nothing is written.
  struct fake_host untransported;
  installed(&untransported);
  CHECK(lina_rollback_run(&untransported.host, &paths, false, NULL) == LINA_ROLLBACK_REFUSED);
  CHECK(untransported.write_count == 0);

  // A guard that cannot be reached reports failure, and a garbled reply is not
  // treated as success.
  struct fake_host unreachable;
  installed(&unreachable);
  struct fake_socket dropped = {&unreachable, true, false, 0};
  struct lina_rollback broken = {&dropped, send_request};
  CHECK(lina_rollback_run(&unreachable.host, &paths, false, &broken) == LINA_ROLLBACK_FAILED);
  struct fake_host garbled;
  installed(&garbled);
  struct fake_socket corrupted = {&garbled, false, true, 0};
  struct lina_rollback bad = {&corrupted, send_request};
  CHECK(lina_rollback_run(&garbled.host, &paths, false, &bad) == LINA_ROLLBACK_FAILED);

  // A refused request from the guard is reported, and the guard wrote nothing.
  uint8_t bad_request[LINA_RESTORE_FRAME];
  memset(bad_request, 0xff, sizeof(bad_request));
  struct fake_host refusing;
  installed(&refusing);
  lina_restore_handle(&refusing.host, &paths, bad_request, reply);
  CHECK(lina_restore_reply_decode(reply, &status) == 0);
  CHECK(status == LINA_RESTORE_REFUSED);
  CHECK(refusing.write_count == 0);
  struct lina_right lina;
  lina_rule_lina(&lina);
  struct fake_right *unchanged = fake_right_find(&refusing, LINA_RIGHT_SCREENSAVER);
  CHECK(unchanged != NULL && lina_right_equal_ignoring_timestamps(&unchanged->definition, &lina));
}
