// SPDX-License-Identifier: Apache-2.0
// The macOS implementation of `struct lina_host`.
//
// The rule goes through Authorization Services, the bundle through
// `SecStaticCode`, launchd through `launchctl` (via the C API where it exists
// and the audited command otherwise), and the session state through
// `CGSessionCopyCurrentDictionary`. Every file is opened with `O_NOFOLLOW` so a
// link cannot redirect a root write.
#include "darwin_host.h"

#include <errno.h>
#include <fcntl.h>
#include <grp.h>
#include <libgen.h>
#include <pthread.h>
#include <spawn.h>
#include <stdarg.h>
#include <stdbool.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <copyfile.h>
#include <sys/stat.h>
#include <sys/sysctl.h>
#include <sys/wait.h>
#include <syslog.h>
#include <time.h>
#include <unistd.h>

#include <CoreFoundation/CoreFoundation.h>
#include <CoreGraphics/CoreGraphics.h>
#include <IOKit/IOKitLib.h>
#include <Security/SecRequirement.h>
#include <Security/SecStaticCode.h>
#include <Security/Authorization.h>
#include <Security/AuthorizationTags.h>

extern char **environ;

// Key names used by the authorization database for a right definition. They
// are documented in the research note as AuthorizationTagsPriv.h names; the
// header itself is not public API, so the strings are repeated here.
static const char *const kClassKey = "class";
static const char *const kRuleKey = "rule";
static const char *const kMechanismsKey = "mechanisms";
static const char *const kKofNKey = "k-of-n";
static const char *const kTriesKey = "tries";
static const char *const kSharedKey = "shared";
static const char *const kVersionKey = "version";

#define LINA_TEAM_ID_DEFAULT ""

#ifndef LINA_TEAM_ID
#define LINA_TEAM_ID LINA_TEAM_ID_DEFAULT
#endif

static int make_dir(void *context, const char *path);

static void log_line(void *context, const char *line) {
  (void)context;
  syslog(LOG_NOTICE, "lina locked-screen: %s", line);
}

static char *cf_copy_string(CFStringRef value) {
  CFIndex length = CFStringGetLength(value);
  CFIndex bytes = CFStringGetMaximumSizeForEncoding(length, kCFStringEncodingUTF8) + 1;
  char *buffer = calloc((size_t)bytes, 1);
  if (buffer == NULL) return NULL;
  if (!CFStringGetCString(value, buffer, bytes, kCFStringEncodingUTF8)) {
    free(buffer);
    return NULL;
  }
  return buffer;
}

static bool cf_to_value(CFTypeRef raw, struct lina_value *out) {
  memset(out, 0, sizeof(*out));
  CFTypeID type = CFGetTypeID(raw);
  if (type == CFStringGetTypeID()) {
    char *text = cf_copy_string((CFStringRef)raw);
    if (text == NULL || strlen(text) >= LINA_RULE_STRING_MAX) {
      free(text);
      return false;
    }
    out->kind = LINA_VALUE_STRING;
    snprintf(out->string, sizeof(out->string), "%s", text);
    free(text);
    return true;
  }
  if (type == CFBooleanGetTypeID()) {
    out->kind = LINA_VALUE_BOOL;
    out->boolean = CFBooleanGetValue((CFBooleanRef)raw);
    return true;
  }
  if (type == CFNumberGetTypeID()) {
    long long number = 0;
    if (!CFNumberGetValue((CFNumberRef)raw, kCFNumberLongLongType, &number)) return false;
    out->kind = LINA_VALUE_INT;
    out->integer = number;
    return true;
  }
  if (type == CFArrayGetTypeID()) {
    CFArrayRef array = (CFArrayRef)raw;
    CFIndex count = CFArrayGetCount(array);
    if (count < 0 || count > LINA_RULE_LIST_MAX) return false;
    out->kind = LINA_VALUE_STRINGS;
    out->count = (uint8_t)count;
    for (CFIndex i = 0; i < count; i++) {
      CFTypeRef item = CFArrayGetValueAtIndex(array, i);
      if (CFGetTypeID(item) != CFStringGetTypeID()) return false;
      char *text = cf_copy_string((CFStringRef)item);
      if (text == NULL || strlen(text) >= LINA_RULE_STRING_MAX) {
        free(text);
        return false;
      }
      snprintf(out->list[i], sizeof(out->list[i]), "%s", text);
      free(text);
    }
    return true;
  }
  return false;
}

static CFTypeRef value_to_cf(const struct lina_value *value) {
  switch (value->kind) {
    case LINA_VALUE_STRING:
      return CFStringCreateWithCString(kCFAllocatorDefault, value->string, kCFStringEncodingUTF8);
    case LINA_VALUE_BOOL:
      return value->boolean ? kCFBooleanTrue : kCFBooleanFalse;
    case LINA_VALUE_INT: {
      long long number = value->integer;
      return CFNumberCreate(kCFAllocatorDefault, kCFNumberLongLongType, &number);
    }
    case LINA_VALUE_STRINGS: {
      CFMutableArrayRef array = CFArrayCreateMutable(kCFAllocatorDefault, value->count, &kCFTypeArrayCallBacks);
      for (uint8_t i = 0; i < value->count; i++) {
        CFStringRef item = CFStringCreateWithCString(kCFAllocatorDefault, value->list[i], kCFStringEncodingUTF8);
        CFArrayAppendValue(array, item);
        CFRelease(item);
      }
      return array;
    }
  }
  return NULL;
}

static int read_right(void *context, const char *name, struct lina_right *out) {
  (void)context;
  CFDictionaryRef stored = NULL;
  OSStatus status = AuthorizationRightGet(name, &stored);
  if (status != errAuthorizationSuccess || stored == NULL) {
    if (stored != NULL) CFRelease(stored);
    // A right that is not in the database is absent, not unreadable.
    return 1;
  }
  lina_right_clear(out);
  CFIndex count = CFDictionaryGetCount(stored);
  const void **keys = calloc((size_t)count, sizeof(void *));
  const void **values = calloc((size_t)count, sizeof(void *));
  CFDictionaryGetKeysAndValues(stored, keys, values);
  bool ok = true;
  for (CFIndex i = 0; i < count && ok; i++) {
    char *key = cf_copy_string((CFStringRef)keys[i]);
    if (key == NULL) {
      ok = false;
      break;
    }
    // Timestamps belong to the database, not to the rule; the backup ignores
    // them and the restore leaves the new ones in place.
    if (strcmp(key, "created") == 0 || strcmp(key, "modified") == 0) {
      free(key);
      continue;
    }
    struct lina_value value;
    if (!cf_to_value(values[i], &value)) {
      free(key);
      ok = false;
      break;
    }
    // The database may hand back a private key set; refuse anything this build
    // cannot write back unchanged.
    const bool known = strcmp(key, kClassKey) == 0 || strcmp(key, kRuleKey) == 0
        || strcmp(key, kMechanismsKey) == 0 || strcmp(key, kKofNKey) == 0
        || strcmp(key, kTriesKey) == 0 || strcmp(key, kSharedKey) == 0
        || strcmp(key, kVersionKey) == 0 || strcmp(key, "comment") == 0
        || strcmp(key, "authenticate-user") == 0 || strcmp(key, "allow-root") == 0
        || strcmp(key, "group") == 0 || strcmp(key, "session-owner") == 0
        || strcmp(key, "timeout") == 0 || strcmp(key, "flags") == 0;
    if (!known) {
      free(key);
      ok = false;
      break;
    }
    if (!lina_right_set_value(out, key, &value)) ok = false;
    free(key);
  }
  free(keys);
  free(values);
  CFRelease(stored);
  return ok ? 0 : -1;
}

static int write_right(void *context, const char *name, const struct lina_right *right) {
  (void)context;
  CFMutableDictionaryRef dictionary = CFDictionaryCreateMutable(kCFAllocatorDefault, 0,
                                                               &kCFTypeDictionaryKeyCallBacks,
                                                               &kCFTypeDictionaryValueCallBacks);
  if (dictionary == NULL) return -1;
  for (uint8_t i = 0; i < right->count; i++) {
    CFStringRef key = CFStringCreateWithCString(kCFAllocatorDefault, right->entries[i].key,
                                                kCFStringEncodingUTF8);
    CFTypeRef value = value_to_cf(&right->entries[i].value);
    if (key == NULL || value == NULL) {
      if (key != NULL) CFRelease(key);
      CFRelease(dictionary);
      return -1;
    }
    CFDictionarySetValue(dictionary, key, value);
    CFRelease(value);
    CFRelease(key);
  }
  CFStringRef description = CFStringCreateWithCString(kCFAllocatorDefault, "LINA locked-screen rule",
                                                      kCFStringEncodingUTF8);
  OSStatus status = AuthorizationRightSet(NULL, name, dictionary, description, NULL, NULL);
  CFRelease(description);
  CFRelease(dictionary);
  return status == errAuthorizationSuccess ? 0 : -1;
}

static int remove_right(void *context, const char *name) {
  (void)context;
  OSStatus status = AuthorizationRightRemove(NULL, name);
  return status == errAuthorizationSuccess ? 0 : 1;
}

struct evaluation {
  const char *right;
  int result;
  bool finished;
  pthread_mutex_t lock;
  pthread_cond_t ready;
};

static void *evaluate_thread(void *argument) {
  struct evaluation *evaluation = argument;
  AuthorizationRef authorization = NULL;
  if (AuthorizationCreate(NULL, kAuthorizationEmptyEnvironment, kAuthorizationFlagDefaults,
                          &authorization) != errAuthorizationSuccess) {
    evaluation->result = -1;
  } else {
    AuthorizationItem item = {(char *)evaluation->right, 0, NULL, 0};
    AuthorizationRights rights = {1, &item};
    AuthorizationFlags flags = kAuthorizationFlagExtendRights | kAuthorizationFlagPreAuthorize;
    OSStatus status = AuthorizationCopyRights(authorization, &rights, kAuthorizationEmptyEnvironment,
                                              flags, NULL);
    evaluation->result = status == errAuthorizationSuccess ? 1 : (status == errAuthorizationDenied ? 0 : -1);
    AuthorizationFree(authorization, kAuthorizationFlagDestroyRights);
  }
  pthread_mutex_lock(&evaluation->lock);
  evaluation->finished = true;
  pthread_cond_signal(&evaluation->ready);
  pthread_mutex_unlock(&evaluation->lock);
  return NULL;
}

static int evaluate_right(void *context, const char *name, uint32_t deadline_ms) {
  (void)context;
  struct evaluation evaluation = {.right = name, .result = -1, .finished = false};
  pthread_mutex_init(&evaluation.lock, NULL);
  pthread_cond_init(&evaluation.ready, NULL);
  pthread_t thread;
  if (pthread_create(&thread, NULL, evaluate_thread, &evaluation) != 0) {
    pthread_mutex_destroy(&evaluation.lock);
    pthread_cond_destroy(&evaluation.ready);
    return -1;
  }
  struct timespec deadline;
  clock_gettime(CLOCK_REALTIME, &deadline);
  deadline.tv_sec += deadline_ms / 1000u;
  deadline.tv_nsec += (long)(deadline_ms % 1000u) * 1000000L;
  if (deadline.tv_nsec >= 1000000000L) {
    deadline.tv_sec += 1;
    deadline.tv_nsec -= 1000000000L;
  }
  pthread_mutex_lock(&evaluation.lock);
  while (!evaluation.finished) {
    if (pthread_cond_timedwait(&evaluation.ready, &evaluation.lock, &deadline) == ETIMEDOUT) break;
  }
  bool finished = evaluation.finished;
  int result = evaluation.result;
  pthread_mutex_unlock(&evaluation.lock);
  if (!finished) {
    // The evaluation is stuck inside the plug-in; the install treats a missing
    // answer as a failed self-test.
    pthread_detach(thread);
    pthread_mutex_destroy(&evaluation.lock);
    pthread_cond_destroy(&evaluation.ready);
    return -1;
  }
  pthread_join(thread, NULL);
  pthread_mutex_destroy(&evaluation.lock);
  pthread_cond_destroy(&evaluation.ready);
  return result;
}

static int copy_bundle(void *context, const char *source, const char *destination) {
  (void)context;
  struct stat info;
  if (lstat(destination, &info) == 0) return -1;
  int status = copyfile(source, destination, NULL, COPYFILE_ALL | COPYFILE_RECURSIVE);
  if (status != 0) {
    // A partly copied bundle would pass for an installed one; remove it.
    char command[1024];
    snprintf(command, sizeof(command), "/bin/rm -rf '%s'", destination);
    (void)system(command);
    return -1;
  }
  return 0;
}

static int verify_code(void *context, const char *path) {
  (void)context;
  CFURLRef url = CFURLCreateFromFileSystemRepresentation(kCFAllocatorDefault, (const UInt8 *)path,
                                                         (CFIndex)strlen(path), true);
  if (url == NULL) return -1;
  SecStaticCodeRef code = NULL;
  OSStatus status = SecStaticCodeCreateWithPath(url, kSecCSDefaultFlags, &code);
  CFRelease(url);
  if (status != errSecSuccess || code == NULL) return -1;
  CFStringRef requirement_text = CFStringCreateWithFormat(
      kCFAllocatorDefault, NULL,
      CFSTR("identifier \"com.lina.locked-screen.unlock\" and anchor apple generic and "
            "certificate leaf[subject.OU] = \"%s\""),
      LINA_TEAM_ID);
  SecRequirementRef requirement = NULL;
  status = SecRequirementCreateWithString(requirement_text, kSecCSDefaultFlags, &requirement);
  CFRelease(requirement_text);
  if (status != errSecSuccess || requirement == NULL) {
    CFRelease(code);
    return -1;
  }
  SecCSFlags flags = kSecCSCheckAllArchitectures | kSecCSCheckNestedCode | kSecCSStrictValidate;
  status = SecStaticCodeCheckValidity(code, flags, requirement);
  CFRelease(requirement);
  CFRelease(code);
  // Anything other than a clean validity result is not trusted: a bundle whose
  // signature cannot be read is treated like an invalid one.
  return status == errSecSuccess ? 1 : 0;
}

static int remove_bundle(void *context, const char *path) {
  (void)context;
  struct stat info;
  if (lstat(path, &info) != 0) return 1;
  if (!S_ISDIR(info.st_mode)) return -1;
  char command[1024];
  snprintf(command, sizeof(command), "/bin/rm -rf '%s'", path);
  return system(command) == 0 ? 0 : -1;
}

static int rename_path(void *context, const char *from, const char *to) {
  (void)context;
  return rename(from, to) == 0 ? 0 : -1;
}

static int run_tool(const char *const *arguments) {
  pid_t child = 0;
  int status = posix_spawn(&child, arguments[0], NULL, NULL, (char *const *)arguments, environ);
  if (status != 0) return -1;
  int waited = 0;
  if (waitpid(child, &waited, 0) < 0) return -1;
  if (!WIFEXITED(waited) || WEXITSTATUS(waited) != 0) return -1;
  return 0;
}

static int install_guard(void *context, const char *binary, const char *plist) {
  struct lina_darwin *darwin = context;
  int source = open(binary, O_RDONLY | O_NOFOLLOW);
  if (source < 0) return -1;
  int target = open(darwin->paths.guard, O_WRONLY | O_CREAT | O_TRUNC | O_NOFOLLOW, 0755);
  if (target < 0) {
    close(source);
    return -1;
  }
  fchmod(target, 0755);
  char buffer[64 * 1024];
  ssize_t read_bytes;
  while ((read_bytes = read(source, buffer, sizeof(buffer))) > 0) {
    ssize_t offset = 0;
    while (offset < read_bytes) {
      ssize_t wrote = write(target, buffer + offset, (size_t)(read_bytes - offset));
      if (wrote <= 0) {
        close(source);
        close(target);
        return -1;
      }
      offset += wrote;
    }
  }
  close(source);
  if (read_bytes < 0 || fsync(target) != 0) {
    close(target);
    return -1;
  }
  close(target);
  char plist_text[4096];
  int source = open(plist, O_RDONLY | O_NOFOLLOW);
  if (source < 0) return -1;
  ssize_t length = read(source, plist_text, sizeof(plist_text) - 1);
  close(source);
  if (length <= 0) return -1;
  plist_text[length] = '\0';
  int target = open(darwin->paths.guard_plist, O_WRONLY | O_CREAT | O_TRUNC | O_NOFOLLOW, 0644);
  if (target < 0) return -1;
  if (write(target, plist_text, (size_t)length) != length) {
    close(target);
    return -1;
  }
  close(target);
  // A job left loaded by an earlier attempt is replaced, not fought over.
  const char *bootout[] = {"/bin/launchctl", "bootout", "system/com.lina.locked-screen.guard", NULL};
  (void)run_tool(bootout);
  const char *bootstrap[] = {"/bin/launchctl", "bootstrap", "system", darwin->paths.guard_plist, NULL};
  return run_tool(bootstrap);
}

static int remove_guard(void *context, const char *binary, const char *plist) {
  struct lina_darwin *darwin = context;
  (void)binary;
  const char *bootout[] = {"/bin/launchctl", "bootout", "system/com.lina.locked-screen.guard", NULL};
  int status = run_tool(bootout);
  if (unlink(darwin->paths.guard_plist) != 0 && errno != ENOENT) return -1;
  if (unlink(darwin->paths.guard) != 0 && errno != ENOENT) return -1;
  (void)plist;
  return status == 0 ? 0 : 1;
}

static int read_file(void *context, const char *path, char *out, size_t capacity, size_t *length) {
  (void)context;
  int fd = open(path, O_RDONLY | O_NOFOLLOW);
  if (fd < 0) return errno == ENOENT ? 0 : -1;
  struct stat info;
  if (fstat(fd, &info) != 0 || !S_ISREG(info.st_mode) || (size_t)info.st_size >= capacity) {
    close(fd);
    return -1;
  }
  ssize_t total = 0;
  while ((size_t)total < capacity) {
    ssize_t got = read(fd, out + total, capacity - (size_t)total);
    if (got < 0) {
      close(fd);
      return -1;
    }
    if (got == 0) break;
    total += got;
  }
  close(fd);
  if ((size_t)total >= capacity) return -1;
  *length = (size_t)total;
  return 1;
}

static int write_file(void *context, const char *path, const void *data, size_t length) {
  struct lina_darwin *darwin = context;
  if (make_dir(NULL, darwin->paths.state_dir) != 0 && errno != EEXIST) return -1;
  int fd = open(path, O_WRONLY | O_CREAT | O_TRUNC | O_NOFOLLOW, 0400);
  if (fd < 0) return -1;
  // A file left by an earlier version keeps its mode; root-only is the point.
  fchmod(fd, 0400);
  size_t written = 0;
  while (written < length) {
    ssize_t wrote = write(fd, (const char *)data + written, length - written);
    if (wrote <= 0) {
      close(fd);
      return -1;
    }
    written += (size_t)wrote;
  }
  if (fsync(fd) != 0) {
    close(fd);
    return -1;
  }
  close(fd);
  return 0;
}

static int remove_file(void *context, const char *path) {
  (void)context;
  if (unlink(path) == 0) return 0;
  return errno == ENOENT ? 1 : -1;
}

static int exists(void *context, const char *path) {
  (void)context;
  struct stat info;
  if (lstat(path, &info) == 0) return 1;
  return errno == ENOENT ? 0 : -1;
}

static int make_dir(void *context, const char *path) {
  (void)context;
  char buffer[LINA_PATH_MAX];
  snprintf(buffer, sizeof(buffer), "%s", path);
  for (char *cursor = buffer + 1; *cursor != '\0'; cursor++) {
    if (*cursor != '/') continue;
    *cursor = '\0';
    if (mkdir(buffer, 0700) != 0 && errno != EEXIST) return -1;
    *cursor = '/';
  }
  if (mkdir(buffer, 0700) != 0 && errno != EEXIST) return -1;
  return 0;
}

static int measure_build(void *context, char *out, size_t capacity) {
  (void)context;
  size_t length = capacity;
  if (sysctlbyname("kern.osversion", out, &length, NULL, 0) != 0 || length == 0) return 0;
  out[length < capacity ? length : capacity - 1] = '\0';
  return 1;
}

static int is_apple_silicon(void *context) {
  (void)context;
  int value = 0;
  size_t length = sizeof(value);
  if (sysctlbyname("hw.optional.arm64", &value, &length, NULL, 0) != 0) return 0;
  return value == 1 ? 1 : 0;
}

static int build_is_measured(void *context, const char *build) {
  struct lina_darwin *darwin = context;
  char text[4096];
  size_t length = 0;
  if (read_file(darwin, darwin->paths.measured, text, sizeof(text), &length) != 1) return 0;
  text[length] = '\0';
  for (char *line = strtok(text, "\n"); line != NULL; line = strtok(NULL, "\n")) {
    if (strcmp(line, build) == 0) return 1;
  }
  return 0;
}

static int eval_test_mac(void *context) {
  struct lina_darwin *darwin = context;
  return exists(darwin, darwin->paths.eval_marker) == 1 ? 1 : 0;
}

// The lock state of the console session. A root system daemon has no GUI
// session of its own, so `CGSessionCopyCurrentDictionary` can answer nothing;
// the registry's console-user entry is the same fact every session sees.
static int locked_from_registry(bool *locked) {
  io_service_t service = IOServiceGetMatchingService(kIOMainPortDefault, IOServiceMatching("IOConsoleUsers"));
  if (service == 0) return -1;
  CFTypeRef users = IORegistryEntryCreateCFProperty(service, CFSTR("IOConsoleUsers"), kCFAllocatorDefault, 0);
  IOObjectRelease(service);
  if (users == NULL) return -1;
  if (CFGetTypeID(users) != CFArrayGetTypeID()) {
    CFRelease(users);
    return -1;
  }
  CFArrayRef array = (CFArrayRef)users;
  int result = -1;
  for (CFIndex i = 0; i < CFArrayGetCount(array); i++) {
    CFTypeRef entry = CFArrayGetValueAtIndex(array, i);
    if (entry == NULL || CFGetTypeID(entry) != CFDictionaryGetTypeID()) continue;
    CFDictionaryRef user = (CFDictionaryRef)entry;
    CFTypeRef on_console = CFDictionaryGetValue(user, CFSTR("CGSSessionOnConsoleKey"));
    if (on_console == NULL || CFGetTypeID(on_console) != CFBooleanGetTypeID()
        || !CFBooleanGetValue((CFBooleanRef)on_console)) {
      continue;
    }
    CFTypeRef value = CFDictionaryGetValue(user, CFSTR("CGSSessionScreenIsLocked"));
    *locked = value != NULL && CFGetTypeID(value) == CFBooleanGetTypeID()
        && CFBooleanGetValue((CFBooleanRef)value);
    result = 0;
    break;
  }
  CFRelease(users);
  return result;
}

static int session_locked(void *context, bool *locked) {
  (void)context;
  CFDictionaryRef session = CGSessionCopyCurrentDictionary();
  if (session != NULL) {
    CFTypeRef value = CFDictionaryGetValue(session, CFSTR("CGSSessionScreenIsLocked"));
    if (value != NULL && CFGetTypeID(value) == CFBooleanGetTypeID()) {
      *locked = CFBooleanGetValue((CFBooleanRef)value);
      CFRelease(session);
      return 0;
    }
    // The key is absent while the session is unlocked.
    *locked = false;
    CFRelease(session);
    return 0;
  }
  return locked_from_registry(locked);
}

static uint64_t monotonic_ms(void *context) {
  (void)context;
  struct timespec now;
  clock_gettime(CLOCK_MONOTONIC, &now);
  return (uint64_t)now.tv_sec * 1000u + (uint64_t)now.tv_nsec / 1000000u;
}

bool lina_darwin_host_init(struct lina_darwin *darwin, const struct lina_paths *paths) {
  memset(darwin, 0, sizeof(*darwin));
  darwin->paths = *paths;
  darwin->host.context = darwin;
  darwin->host.read_right = read_right;
  darwin->host.write_right = write_right;
  darwin->host.remove_right = remove_right;
  darwin->host.evaluate_right = evaluate_right;
  darwin->host.copy_bundle = copy_bundle;
  darwin->host.verify_code = verify_code;
  darwin->host.remove_bundle = remove_bundle;
  darwin->host.rename_path = rename_path;
  darwin->host.install_guard = install_guard;
  darwin->host.remove_guard = remove_guard;
  darwin->host.read_file = read_file;
  darwin->host.write_file = write_file;
  darwin->host.remove_file = remove_file;
  darwin->host.exists = exists;
  darwin->host.make_dir = make_dir;
  darwin->host.measure_build = measure_build;
  darwin->host.is_apple_silicon = is_apple_silicon;
  darwin->host.build_is_measured = build_is_measured;
  darwin->host.eval_test_mac = eval_test_mac;
  darwin->host.session_locked = session_locked;
  darwin->host.monotonic_ms = monotonic_ms;
  darwin->host.log_line = log_line;
  return true;
}
