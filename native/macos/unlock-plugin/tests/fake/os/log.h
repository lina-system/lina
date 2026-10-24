// SPDX-License-Identifier: Apache-2.0
// A test stand-in for <os/log.h>. The plug-in writes its answer to the system
// log with os_log; this captures that line so the tests can read it without
// macOS.
#ifndef LINA_FAKE_OS_LOG_H
#define LINA_FAKE_OS_LOG_H

typedef struct lina_fake_os_log *os_log_t;

typedef enum {
  OS_LOG_TYPE_DEFAULT = 0,
  OS_LOG_TYPE_INFO,
  OS_LOG_TYPE_DEBUG,
  OS_LOG_TYPE_ERROR,
  OS_LOG_TYPE_FAULT
} os_log_type_t;

#define OS_LOG_DEFAULT ((os_log_t)0)

void os_log_with_type(os_log_t log, os_log_type_t type, const char *format, ...);

#endif
