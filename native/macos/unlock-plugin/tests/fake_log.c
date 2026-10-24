// SPDX-License-Identifier: Apache-2.0
// Capture the system-log line the plug-in writes, so the tests can read the
// answer without macOS. os_log's public annotation is stripped before the line
// is formatted.
#include <stdarg.h>
#include <stdio.h>
#include <string.h>

#include <os/log.h>

#include "fake_log.h"

#define LINA_FAKE_LOG_LINE_MAX 512

static int lines;
static char last[LINA_FAKE_LOG_LINE_MAX];

static void plain_format(const char *format, char *out, size_t size) {
  const char annotation[] = "%{public}";
  const size_t length = sizeof annotation - 1;
  size_t used = 0;
  for (size_t index = 0; format[index] != '\0' && used + 1 < size;) {
    if (strncmp(format + index, annotation, length) == 0) {
      out[used++] = '%';
      index += length;
    } else {
      out[used++] = format[index++];
    }
  }
  out[used] = '\0';
}

void os_log_with_type(os_log_t log, os_log_type_t type, const char *format, ...) {
  (void)log;
  (void)type;
  char plain[LINA_FAKE_LOG_LINE_MAX];
  va_list arguments;
  plain_format(format, plain, sizeof plain);
  va_start(arguments, format);
  vsnprintf(last, sizeof last, plain, arguments);
  va_end(arguments);
  lines++;
}

void fake_log_reset(void) {
  lines = 0;
  last[0] = '\0';
}

int fake_log_lines(void) {
  return lines;
}

const char *fake_log_last(void) {
  return last;
}
