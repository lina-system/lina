// SPDX-License-Identifier: Apache-2.0
// The guard daemon and the rollback command share this binary.
//
//   com.lina.locked-screen.guard            the daemon launchd starts at load
//   com.lina.locked-screen.guard restore    the rollback command
//
// The daemon serves the restore request on a root-owned socket that the admin
// group may use, and runs one guard pass at load, on every change of the
// plug-in folder, and once a second so the install's two-minute check and the
// measured-build check are never late by more than that.
#include <errno.h>
#include <fcntl.h>
#include <grp.h>
#include <pwd.h>
#include <signal.h>
#include <stdbool.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/event.h>
#include <sys/socket.h>
#include <sys/stat.h>
#include <sys/time.h>
#include <sys/un.h>
#include <sys/wait.h>
#include <unistd.h>

#include "darwin_host.h"
#include "guard.h"
#include "restore.h"

#define PLUGIN_DIRECTORY "/Library/Security/SecurityAgentPlugins"
#define GUARD_POLL_MS 1000
#define GUARD_REQUEST_TIMEOUT_MS 2000

static volatile sig_atomic_t stopping = 0;

static void on_signal(int signal_number) {
  (void)signal_number;
  stopping = 1;
}

static gid_t admin_group(void) {
  struct group *admin = getgrnam("admin");
  return admin == NULL ? (gid_t)-1 : admin->gr_gid;
}

static bool uid_is_admin(uid_t uid) {
  if (uid == 0) return true;
  gid_t admin = admin_group();
  if (admin == (gid_t)-1) return false;
  struct passwd *user = getpwuid(uid);
  if (user == NULL) return false;
  int count = 16;
  gid_t groups[16];
  if (getgrouplist(user->pw_name, user->pw_gid, groups, &count) < 0) return false;
  for (int i = 0; i < count; i++) {
    if (groups[i] == admin) return true;
  }
  return false;
}

// Reads exactly `length` bytes, or fails. The caller sets the deadline with
// SO_RCVTIMEO/SO_SNDTIMEO, so a stalled peer cannot hold the guard.
static bool read_exact(int fd, void *buffer, size_t length) {
  size_t done = 0;
  while (done < length) {
    ssize_t got = read(fd, (char *)buffer + done, length - done);
    if (got <= 0) return false;
    done += (size_t)got;
  }
  return true;
}

static bool write_exact(int fd, const void *buffer, size_t length) {
  size_t done = 0;
  while (done < length) {
    ssize_t wrote = write(fd, (const char *)buffer + done, length - done);
    if (wrote <= 0) return false;
    done += (size_t)wrote;
  }
  return true;
}

// One request per call: the caller returns to its loop, so a burst of requests
// cannot hold the guard past a tick.
static void serve_one(int listener, const struct lina_host *host, const struct lina_paths *paths) {
  int fd = accept(listener, NULL, NULL);
  if (fd < 0) return;
  {
    struct timeval timeout = {GUARD_REQUEST_TIMEOUT_MS / 1000, (GUARD_REQUEST_TIMEOUT_MS % 1000) * 1000};
    setsockopt(fd, SOL_SOCKET, SO_RCVTIMEO, &timeout, sizeof(timeout));
    setsockopt(fd, SOL_SOCKET, SO_SNDTIMEO, &timeout, sizeof(timeout));
    uid_t uid = (uid_t)-1;
    gid_t gid = (gid_t)-1;
    uint8_t request[LINA_RESTORE_FRAME];
    uint8_t reply[LINA_RESTORE_FRAME];
    if (getpeereid(fd, &uid, &gid) == 0 && uid_is_admin(uid) && read_exact(fd, request, sizeof(request))) {
      lina_restore_handle(host, paths, request, reply);
    } else {
      lina_restore_reply_encode(LINA_RESTORE_REFUSED, reply);
    }
    write_exact(fd, reply, sizeof(reply));
    close(fd);
    (void)gid;
  }
}

static int listen_on(const char *path) {
  struct sockaddr_un address;
  memset(&address, 0, sizeof(address));
  address.sun_family = AF_UNIX;
  if (strlen(path) >= sizeof(address.sun_path)) return -1;
  snprintf(address.sun_path, sizeof(address.sun_path), "%s", path);
  char directory[LINA_PATH_MAX];
  snprintf(directory, sizeof(directory), "%s", path);
  char *slash = strrchr(directory, '/');
  if (slash != NULL) {
    *slash = '\0';
    if (mkdir(directory, 0755) != 0 && errno != EEXIST) return -1;
  }
  unlink(path);
  int fd = socket(AF_UNIX, SOCK_STREAM, 0);
  if (fd < 0) return -1;
  if (bind(fd, (struct sockaddr *)&address, sizeof(address)) != 0) {
    close(fd);
    return -1;
  }
  gid_t admin = admin_group();
  if (admin != (gid_t)-1) {
    if (chown(path, 0, admin) != 0 || chmod(path, 0660) != 0) {
      close(fd);
      unlink(path);
      return -1;
    }
  }
  if (listen(fd, 8) != 0) {
    close(fd);
    unlink(path);
    return -1;
  }
  return fd;
}

static int guard_daemon(struct lina_darwin *darwin) {
  int listener = listen_on(LINA_GUARD_SOCKET);
  if (listener < 0) {
    darwin->host.log_line(darwin->host.context, "guard cannot serve the restore socket");
    return 1;
  }
  int directory = open(PLUGIN_DIRECTORY, O_RDONLY | O_DIRECTORY);
  int kqueue_fd = kqueue();
  if (kqueue_fd < 0) return 1;
  if (directory >= 0) {
    struct kevent change;
    EV_SET(&change, (uintptr_t)directory, EVFILT_VNODE, EV_ADD | EV_CLEAR,
           NOTE_WRITE | NOTE_DELETE | NOTE_RENAME | NOTE_REVOKE | NOTE_EXTEND, 0, NULL);
    (void)kevent(kqueue_fd, &change, 1, NULL, 0, NULL);
  }
  struct kevent registration;
  EV_SET(&registration, (uintptr_t)listener, EVFILT_READ, EV_ADD, 0, 0, NULL);
  if (kevent(kqueue_fd, &registration, 1, NULL, 0, NULL) != 0) return 1;

  struct sigaction action;
  memset(&action, 0, sizeof(action));
  action.sa_handler = on_signal;
  sigaction(SIGTERM, &action, NULL);
  sigaction(SIGINT, &action, NULL);
  signal(SIGPIPE, SIG_IGN);

  while (!stopping) {
    struct timespec timeout = {GUARD_POLL_MS / 1000, (GUARD_POLL_MS % 1000) * 1000000L};
    struct kevent events[4];
    int ready = kevent(kqueue_fd, NULL, 0, events, 4, &timeout);
    if (ready < 0) {
      if (errno == EINTR) continue;
      break;
    }
    for (int i = 0; i < ready; i++) {
      if ((int)events[i].ident == listener) serve_one(listener, &darwin->host, &darwin->paths);
      // A folder change needs no separate action: the pass below reads the
      // bundle and the rule again.
    }
    // A socket request serves one connection at a time; a burst is answered by
    // the next pass of the loop.
    uint64_t now = darwin->host.monotonic_ms(darwin->host.context);
    struct lina_guard_report report = lina_guard_tick(&darwin->host, &darwin->paths, now);
    if (report.action == LINA_GUARD_RESTORED) {
      darwin->host.log_line(darwin->host.context, report.reason == NULL ? "restored" : report.reason);
    }
  }
  close(listener);
  unlink(LINA_GUARD_SOCKET);
  if (directory >= 0) close(directory);
  close(kqueue_fd);
  return 0;
}

// The rollback command's transport: one frame each way, no Node, no Core and no
// network, just this socket. A guard that cannot be reached is a failure, not a
// refusal: the person is told to ask an administrator for the root command.
static int send_request(void *context, const uint8_t request[LINA_RESTORE_FRAME],
                        uint8_t reply[LINA_RESTORE_FRAME], bool *refused) {
  (void)context;
  *refused = false;
  int fd = socket(AF_UNIX, SOCK_STREAM, 0);
  if (fd < 0) return -1;
  struct sockaddr_un address;
  memset(&address, 0, sizeof(address));
  address.sun_family = AF_UNIX;
  snprintf(address.sun_path, sizeof(address.sun_path), "%s", LINA_GUARD_SOCKET);
  if (connect(fd, (struct sockaddr *)&address, sizeof(address)) != 0) {
    close(fd);
    return -1;
  }
  bool ok = write_exact(fd, request, LINA_RESTORE_FRAME) && read_exact(fd, reply, LINA_RESTORE_FRAME);
  close(fd);
  return ok ? 0 : -1;
}

static int restore_command(struct lina_darwin *darwin) {
  bool root = geteuid() == 0;
  struct lina_rollback transport = {NULL, send_request};
  enum lina_rollback_result result =
      lina_rollback_run(&darwin->host, &darwin->paths, root, &transport);
  switch (result) {
    case LINA_ROLLBACK_RESTORED:
      printf("The lock screen rule is back to what it was before LINA.\n");
      return 0;
    case LINA_ROLLBACK_REFUSED:
      printf("The guard refused the restore. Ask an administrator to run this command as root.\n");
      return 2;
    case LINA_ROLLBACK_FAILED:
      printf("The restore did not complete. Ask an administrator to run this command as root.\n");
      return 1;
  }
  return 1;
}

int lina_guard_main(int argc, char **argv) {
  struct lina_paths paths;
  lina_paths_default(&paths);
  struct lina_darwin darwin;
  if (!lina_darwin_host_init(&darwin, &paths)) return 1;
  if (argc >= 2 && strcmp(argv[1], "restore") == 0) return restore_command(&darwin);
  if (argc >= 2) {
    fprintf(stderr, "usage: %s [restore]\n", argv[0]);
    return 2;
  }
  return guard_daemon(&darwin);
}
