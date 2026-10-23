// SPDX-License-Identifier: Apache-2.0
// The captured system-log line from the plug-in's last answer.
#ifndef LINA_FAKE_LOG_H
#define LINA_FAKE_LOG_H

void fake_log_reset(void);
int fake_log_lines(void);
const char *fake_log_last(void);

#endif
