// SPDX-License-Identifier: Apache-2.0
// `lina-locked-screen install|uninstall|restore`
//
// The install needs administrator rights. LINA has no password prompt of its
// own: the command re-runs itself through macOS's own dialog, and the password
// never enters LINA's process.
#include <errno.h>
#include <fcntl.h>
#include <libgen.h>
#include <limits.h>
#include <spawn.h>
#include <mach-o/dyld.h>
#include <stdarg.h>
#include <stdbool.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/stat.h>
#include <sys/wait.h>
#include <unistd.h>

#include "darwin_host.h"
#include "install.h"
#include "restore.h"
#include "state.h"

extern char **environ;

// The guard binary's entry point; the rollback command is the same restore.
int lina_guard_main(int argc, char **argv);

static void usage(const char *program) {
  fprintf(stderr,
          "usage: %s install --bundle <path> --guard <path> --plist <path> --socket <broker socket>"
          " --uid <n> --deadline <ms> [--measured <path>]\n"
          "       %s uninstall\n"
          "       %s restore\n",
          program, program, program);
}

static bool quote(const char *value, char *out, size_t capacity, size_t *used) {
  if (*used + 2 >= capacity) return false;
  out[(*used)++] = '\'';
  for (const char *cursor = value; *cursor != '\0'; cursor++) {
    if (*cursor == '\'') {
      if (*used + 4 >= capacity) return false;
      memcpy(out + *used, "'\\''", 4);
      *used += 4;
    } else {
      if (*used + 1 >= capacity) return false;
      out[(*used)++] = *cursor;
    }
  }
  if (*used + 2 >= capacity) return false;
  out[(*used)++] = '\'';
  out[*used] = '\0';
  return true;
}

// macOS asks for the administrator's password in its own dialog. The shell
// command is built from the already parsed arguments, each single quoted.
static int rerun_as_root(int argc, char **argv) {
  char command[4096];
  size_t used = 0;
  command[0] = '\0';
  // The privileged shell starts in another directory, so relative arguments
  // keep their meaning.
  char directory[PATH_MAX];
  if (getcwd(directory, sizeof(directory)) == NULL) return 1;
  const char *prefix = "cd ";
  if (strlen(prefix) >= sizeof(command)) return 1;
  memcpy(command, prefix, strlen(prefix));
  used = strlen(prefix);
  if (!quote(directory, command, sizeof(command), &used)) return 1;
  const char *and = " && LINA_PRIVILEGED=1 ";
  if (used + strlen(and) >= sizeof(command)) return 1;
  memcpy(command + used, and, strlen(and));
  used += strlen(and);
  command[used] = '\0';
  char executable[PATH_MAX];
  uint32_t size = sizeof(executable);
  if (_NSGetExecutablePath(executable, &size) != 0) return 1;
  if (!quote(executable, command, sizeof(command), &used)) return 1;
  for (int i = 1; i < argc; i++) {
    if (used + 1 >= sizeof(command)) return 1;
    command[used++] = ' ';
    command[used] = '\0';
    if (!quote(argv[i], command, sizeof(command), &used)) return 1;
  }
  // The AppleScript string escapes the two characters that end it.
  char script[8192];
  size_t script_used = 0;
  script_used += (size_t)snprintf(script, sizeof(script), "do shell script \"");
  for (const char *cursor = command; *cursor != '\0'; cursor++) {
    if (*cursor == '"' || *cursor == '\\') {
      if (script_used + 2 >= sizeof(script)) return 1;
      script[script_used++] = '\\';
      script[script_used++] = *cursor;
    } else {
      if (script_used + 1 >= sizeof(script)) return 1;
      script[script_used++] = *cursor;
    }
  }
  script[script_used] = '\0';
  if (script_used + 20 >= sizeof(script)) return 1;
  snprintf(script + script_used, sizeof(script) - script_used, "\" with administrator privileges");

  pid_t child = 0;
  char *const arguments[] = {"/usr/bin/osascript", "-e", script, NULL};
  if (posix_spawn(&child, arguments[0], NULL, NULL, arguments, environ) != 0) return 1;
  int status = 0;
  if (waitpid(child, &status, 0) < 0) return 1;
  if (!WIFEXITED(status)) return 1;
  return WEXITSTATUS(status);
}

static const char *argument(int argc, char **argv, const char *name) {
  for (int i = 0; i + 1 < argc; i++) {
    if (strcmp(argv[i], name) == 0) return argv[i + 1];
  }
  return NULL;
}

static int install_command(int argc, char **argv, struct lina_darwin *darwin) {
  const char *bundle = argument(argc, argv, "--bundle");
  const char *guard = argument(argc, argv, "--guard");
  const char *plist = argument(argc, argv, "--plist");
  const char *socket_path = argument(argc, argv, "--socket");
  const char *uid = argument(argc, argv, "--uid");
  const char *deadline = argument(argc, argv, "--deadline");
  const char *measured = argument(argc, argv, "--measured");
  if (bundle == NULL || guard == NULL || plist == NULL || socket_path == NULL || uid == NULL
      || deadline == NULL) {
    usage(argv[0]);
    return 2;
  }
  struct stat info;
  if (stat(bundle, &info) != 0 || !S_ISDIR(info.st_mode)) {
    fprintf(stderr, "The plug-in is not where this build expects it.\n");
    return 1;
  }
  struct lina_install_input input = {
      .bundle_source = bundle,
      .guard_binary = guard,
      .guard_plist = plist,
      .measured_source = measured,
      .broker_socket = socket_path,
      .person_uid = (uint32_t)strtoul(uid, NULL, 10),
      .deadline_ms = (uint32_t)strtoul(deadline, NULL, 10),
  };
  struct lina_outcome outcome = lina_install(&darwin->host, &darwin->paths, &input);
  switch (outcome.code) {
    case LINA_INSTALL_OK:
      printf("Locked-Mac work is installed. Lock the Mac and unlock it once by hand within two minutes.\n");
      return 0;
    case LINA_INSTALL_RULE:
      printf("The lock screen rule belongs to something else (%s). Nothing was changed.\n",
             outcome.reason == NULL ? "unknown" : outcome.reason);
      return 1;
    case LINA_INSTALL_BUILD:
      printf("This macOS build has not passed the locked-Mac check yet. Nothing was changed.\n");
      return 1;
    case LINA_INSTALL_DEVICE:
      printf("Locked-Mac work needs an Apple silicon Mac. Nothing was changed.\n");
      return 1;
    case LINA_INSTALL_SELFTEST:
      printf("The plug-in did not answer the self-test, so the install stopped and everything it wrote was removed.\n");
      return 1;
    case LINA_INSTALL_SIGNATURE:
      printf("The plug-in's signature is not valid, so the install stopped and everything it wrote was removed.\n");
      return 1;
    case LINA_INSTALL_READBACK:
      printf("The lock screen rule did not read back as written, so the earlier rule was restored.\n");
      return 1;
    default:
      printf("The install stopped (%s) and everything it wrote was removed.\n",
             outcome.reason == NULL ? "unknown" : outcome.reason);
      return 1;
  }
}

static int uninstall_command(struct lina_darwin *darwin) {
  struct lina_outcome outcome;
  int code = lina_uninstall(&darwin->host, &darwin->paths, &outcome);
  if (code == LINA_UNINSTALL_OK) {
    printf("Locked-Mac work is removed and the lock screen rule is back to what it was before LINA.\n");
    return 0;
  }
  if (code == LINA_UNINSTALL_DEFERRED) {
    printf("The lock screen rule was changed by something else since LINA wrote it. "
           "The rule was left alone, and LINA's unlock right and plug-in stay so the rule keeps working.\n");
    return 1;
  }
  printf("The uninstall did not complete. Nothing else was changed.\n");
  return 1;
}

int main(int argc, char **argv) {
  if (argc < 2) {
    usage(argv[0]);
    return 2;
  }
  struct lina_paths paths;
  lina_paths_default(&paths);
  struct lina_darwin darwin;
  if (!lina_darwin_host_init(&darwin, &paths)) {
    fprintf(stderr, "This Mac is not supported.\n");
    return 1;
  }
  if (strcmp(argv[1], "restore") == 0) {
    // The rollback command is the same restore, and needs no administrator
    // password from the person's own account.
    char *restore_argv[] = {(char *)"guard", (char *)"restore", NULL};
    return lina_guard_main(2, restore_argv);
  }
  if (strcmp(argv[1], "install") != 0 && strcmp(argv[1], "uninstall") != 0) {
    usage(argv[0]);
    return 2;
  }
  if (geteuid() != 0) {
    if (getenv("LINA_PRIVILEGED") != NULL) {
      fprintf(stderr, "This command needs administrator rights.\n");
      return 1;
    }
    return rerun_as_root(argc, argv);
  }
  if (strcmp(argv[1], "install") == 0) return install_command(argc, argv, &darwin);
  return uninstall_command(&darwin);
}
