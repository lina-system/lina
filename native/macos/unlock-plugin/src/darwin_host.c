// SPDX-License-Identifier: Apache-2.0
// The bundle builds with -std=c11, which hides the POSIX and Darwin
// declarations these calls live behind unless they are requested.
#define _POSIX_C_SOURCE 200809L
#define _DARWIN_C_SOURCE 1

#include "darwin_host.h"

#include <CoreFoundation/CoreFoundation.h>
#include <Security/Security.h>
#include <bsm/libbsm.h>
#include <errno.h>
#include <fcntl.h>
#include <poll.h>
#include <stdlib.h>
#include <string.h>
#include <sys/socket.h>
#include <sys/stat.h>
#include <sys/un.h>
#include <time.h>
#include <unistd.h>

// The installer writes these; the guard restores them. The directory is
// root-only, so the person's own account cannot arm the feature or clear the
// crash marker.
#define LINA_STATE_DIRECTORY "/var/db/lina/locked-screen"
#define LINA_CONFIG_PATH LINA_STATE_DIRECTORY "/enabled"
#define LINA_MARKER_PATH LINA_STATE_DIRECTORY "/evaluation"

#ifndef LINA_BROKER_ID
#define LINA_BROKER_ID "com.lina.lock-broker"
#endif
#ifndef LINA_TEAM_ID
#define LINA_TEAM_ID "TEAMID"
#endif

// The broker's running code must be Apple-issued for LINA's team and carry the
// broker's identifier. The hardened runtime and the debugging entitlement are
// read separately, so each refusal keeps its own reason.
static const char broker_requirement[] = "anchor apple generic and identifier \"" LINA_BROKER_ID
                                         "\" and certificate leaf[subject.OU] = \"" LINA_TEAM_ID "\"";

static uint64_t monotonic_ms(void) {
  struct timespec now;
  if (clock_gettime(CLOCK_MONOTONIC, &now) != 0) return 0;
  return (uint64_t)now.tv_sec * 1000u + (uint64_t)now.tv_nsec / 1000000u;
}

static int read_config(void *context, struct lina_config *out) {
  (void)context;
  memset(out, 0, sizeof *out);
  out->deadline_ms = LINA_DEFAULT_DEADLINE_MS;
  int fd = open(LINA_CONFIG_PATH, O_RDONLY | O_CLOEXEC);
  if (fd < 0) return errno == ENOENT ? 0 : -1;
  char buffer[1024];
  ssize_t length = read(fd, buffer, sizeof buffer - 1);
  close(fd);
  if (length <= 0) return -1;
  buffer[length] = '\0';

  bool socket_set = false;
  bool uid_set = false;
  char *cursor = buffer;
  while (*cursor != '\0') {
    char *line = cursor;
    char *newline = strchr(cursor, '\n');
    if (newline != NULL) {
      *newline = '\0';
      cursor = newline + 1;
    } else {
      cursor += strlen(cursor);
    }
    char *equals = strchr(line, '=');
    if (equals == NULL) continue;
    *equals = '\0';
    const char *key = line;
    const char *value = equals + 1;
    if (strcmp(key, "socket") == 0) {
      if (strlen(value) >= sizeof out->socket_path) return -1;
      strcpy(out->socket_path, value);
      socket_set = true;
    } else if (strcmp(key, "uid") == 0) {
      char *end = NULL;
      unsigned long parsed = strtoul(value, &end, 10);
      if (end == value || *end != '\0') return -1;
      out->person_uid = (uint32_t)parsed;
      uid_set = true;
    } else if (strcmp(key, "deadline_ms") == 0) {
      char *end = NULL;
      unsigned long parsed = strtoul(value, &end, 10);
      if (end == value || *end != '\0') return -1;
      out->deadline_ms = (uint32_t)parsed;
    }
  }
  if (!socket_set || !uid_set) return -1;
  out->enabled = true;
  return 0;
}

static int crash_marker_present(void *context) {
  (void)context;
  struct stat info;
  if (stat(LINA_MARKER_PATH, &info) == 0) return 1;
  return errno == ENOENT ? 0 : -1;
}

static int write_crash_marker(void *context) {
  (void)context;
  // Exclusive creation keeps two concurrent evaluations from sharing a marker:
  // the second one fails here and denies.
  int fd = open(LINA_MARKER_PATH, O_WRONLY | O_CREAT | O_EXCL | O_CLOEXEC, 0600);
  if (fd < 0) return -1;
  int synced = fsync(fd);
  int closed = close(fd);
  return synced == 0 && closed == 0 ? 0 : -1;
}

static void clear_crash_marker(void *context) {
  (void)context;
  unlink(LINA_MARKER_PATH);
}

static bool wait_fd(int fd, short events, uint32_t timeout_ms, bool *timed_out) {
  const uint64_t start = monotonic_ms();
  for (;;) {
    uint64_t elapsed = monotonic_ms() - start;
    if (elapsed >= timeout_ms) {
      *timed_out = true;
      return false;
    }
    struct pollfd entry;
    entry.fd = fd;
    entry.events = events;
    entry.revents = 0;
    int ready = poll(&entry, 1, (int)(timeout_ms - elapsed));
    if (ready > 0) return true;
    if (ready == 0) {
      *timed_out = true;
      return false;
    }
    if (errno != EINTR) return false;
  }
}

static int connect_broker(void *context, const char *path, uint32_t timeout_ms, bool *timed_out) {
  (void)context;
  int fd = socket(AF_UNIX, SOCK_STREAM, 0);
  if (fd < 0) return -1;
  if (fcntl(fd, F_SETFD, FD_CLOEXEC) != 0) {
    close(fd);
    return -1;
  }
  // A broker that closes after the checks must yield a deny, not SIGPIPE in
  // the privileged host.
  int no_sigpipe = 1;
  if (setsockopt(fd, SOL_SOCKET, SO_NOSIGPIPE, &no_sigpipe, sizeof no_sigpipe) != 0) {
    close(fd);
    return -1;
  }
  struct sockaddr_un address;
  memset(&address, 0, sizeof address);
  address.sun_family = AF_UNIX;
  if (strlen(path) >= sizeof address.sun_path) {
    close(fd);
    return -1;
  }
  strcpy(address.sun_path, path);
  int flags = fcntl(fd, F_GETFL, 0);
  if (flags < 0 || fcntl(fd, F_SETFL, flags | O_NONBLOCK) != 0) {
    close(fd);
    return -1;
  }
  if (connect(fd, (struct sockaddr *)&address, sizeof address) == 0) return fd;
  if (errno != EINPROGRESS) {
    close(fd);
    return -1;
  }
  if (!wait_fd(fd, POLLOUT, timeout_ms, timed_out)) {
    close(fd);
    return -1;
  }
  int error = 0;
  socklen_t length = sizeof error;
  if (getsockopt(fd, SOL_SOCKET, SO_ERROR, &error, &length) != 0 || error != 0) {
    close(fd);
    return -1;
  }
  return fd;
}

static int peer_token(void *context, int fd, struct lina_token *out) {
  (void)context;
  audit_token_t token;
  memset(&token, 0, sizeof token);
  socklen_t length = sizeof token;
  if (getsockopt(fd, SOL_LOCAL, LOCAL_PEERTOKEN, &token, &length) != 0) return -1;
  if (length != (socklen_t)sizeof token || sizeof token != LINA_TOKEN_BYTES) return -1;
  memcpy(out->bytes, &token, sizeof token);
  out->auid = (uint32_t)audit_token_to_auid(token);
  out->euid = (uint32_t)audit_token_to_euid(token);
  out->pid = (int32_t)audit_token_to_pid(token);
  return 0;
}

static int inspect_peer(void *context, const struct lina_token *token, uint32_t person_uid,
                        struct lina_peer_report *out) {
  (void)context;
  memset(out, 0, sizeof *out);
  CFDataRef token_data = CFDataCreate(NULL, token->bytes, LINA_TOKEN_BYTES);
  if (token_data == NULL) return -1;
  const void *keys[] = { kSecGuestAttributeAudit };
  const void *values[] = { token_data };
  CFDictionaryRef attributes =
      CFDictionaryCreate(NULL, keys, values, 1, &kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
  CFRelease(token_data);
  if (attributes == NULL) return -1;
  SecCodeRef code = NULL;
  OSStatus status = SecCodeCopyGuestWithAttributes(NULL, attributes, kSecCSDefaultFlags, &code);
  CFRelease(attributes);
  if (status != errSecSuccess || code == NULL) return -1;

  int result = -1;
  CFStringRef requirement_text = CFStringCreateWithCString(NULL, broker_requirement, kCFStringEncodingUTF8);
  SecRequirementRef requirement = NULL;
  if (requirement_text == NULL) goto done;
  status = SecRequirementCreateWithString(requirement_text, kSecCSDefaultFlags, &requirement);
  CFRelease(requirement_text);
  if (status != errSecSuccess || requirement == NULL) goto done;
  out->signature_ok = SecCodeCheckValidity(code, kSecCSDefaultFlags, requirement) == errSecSuccess;
  CFRelease(requirement);
  if (!out->signature_ok) {
    result = 0;
    goto done;
  }

  CFDictionaryRef information = NULL;
  status = SecCodeCopySigningInformation(code, kSecCSSigningInformation, &information);
  if (status != errSecSuccess || information == NULL) goto done;
  CFNumberRef flags = CFDictionaryGetValue(information, kSecCodeInfoFlags);
  SInt64 value = 0;
  if (flags == NULL || !CFNumberGetValue(flags, kCFNumberSInt64Type, &value)) {
    CFRelease(information);
    goto done;
  }
  out->hardened_runtime = (value & kSecCodeSignatureRuntime) != 0;
  CFDictionaryRef entitlements = CFDictionaryGetValue(information, kSecCodeInfoEntitlementsDict);
  if (entitlements != NULL) {
    CFBooleanRef debugger = CFDictionaryGetValue(entitlements, CFSTR("com.apple.security.get-task-allow"));
    out->get_task_allow = debugger != NULL && CFBooleanGetValue(debugger);
  }
  CFRelease(information);
  out->same_user = token->auid == person_uid;
  result = 0;

done:
  CFRelease(code);
  return result;
}

static uint64_t host_monotonic_ms(void *context) {
  (void)context;
  return monotonic_ms();
}

static int random_bytes(void *context, uint8_t *out, size_t len) {
  (void)context;
  return SecRandomCopyBytes(kSecRandomDefault, len, out) == errSecSuccess ? 0 : -1;
}

static int write_all(void *context, int fd, const void *buf, size_t len, uint32_t timeout_ms, bool *timed_out) {
  (void)context;
  const uint8_t *bytes = buf;
  size_t sent = 0;
  const uint64_t start = monotonic_ms();
  while (sent < len) {
    ssize_t written = write(fd, bytes + sent, len - sent);
    if (written > 0) {
      sent += (size_t)written;
      continue;
    }
    if (written < 0 && errno == EINTR) continue;
    if (written < 0 && (errno == EAGAIN || errno == EWOULDBLOCK)) {
      uint64_t elapsed = monotonic_ms() - start;
      if (elapsed >= timeout_ms) {
        *timed_out = true;
        return -1;
      }
      if (!wait_fd(fd, POLLOUT, (uint32_t)(timeout_ms - elapsed), timed_out)) return -1;
      continue;
    }
    return -1;
  }
  return 0;
}

static int read_all(void *context, int fd, void *buf, size_t len, uint32_t timeout_ms, bool *timed_out) {
  (void)context;
  uint8_t *bytes = buf;
  size_t received = 0;
  const uint64_t start = monotonic_ms();
  while (received < len) {
    ssize_t count = read(fd, bytes + received, len - received);
    if (count > 0) {
      received += (size_t)count;
      continue;
    }
    if (count == 0) return -1;
    if (errno == EINTR) continue;
    if (errno == EAGAIN || errno == EWOULDBLOCK) {
      uint64_t elapsed = monotonic_ms() - start;
      if (elapsed >= timeout_ms) {
        *timed_out = true;
        return -1;
      }
      if (!wait_fd(fd, POLLIN, (uint32_t)(timeout_ms - elapsed), timed_out)) return -1;
      continue;
    }
    return -1;
  }
  return 0;
}

static void close_fd(void *context, int fd) {
  (void)context;
  close(fd);
}

static const struct lina_host darwin_host = {
  .context = NULL,
  .load_config = read_config,
  .crash_marker_present = crash_marker_present,
  .write_crash_marker = write_crash_marker,
  .clear_crash_marker = clear_crash_marker,
  .connect_broker = connect_broker,
  .peer_token = peer_token,
  .inspect_peer = inspect_peer,
  .monotonic_ms = host_monotonic_ms,
  .random_bytes = random_bytes,
  .write_all = write_all,
  .read_all = read_all,
  .close_fd = close_fd,
};

const struct lina_host *lina_darwin_host(void) {
  return &darwin_host;
}
