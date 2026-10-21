// SPDX-License-Identifier: Apache-2.0
package telegram

import (
	"context"
	"testing"
	"time"
)

func TestReadRecoveryClassification(t *testing.T) {
	for _, err := range []error{errAdmission, errTransport, &failure{class: "unknown"}} {
		if delay, retry := RecoveryDelay(err); !retry || delay <= 0 {
			t.Fatal("recoverable poll admission was abandoned")
		}
	}
	if delay, retry := RecoveryDelay(&failure{class: "retry_later", retryAfter: time.Minute}); !retry || delay < time.Minute {
		t.Fatal("retry-after ignored")
	}
	for _, err := range []error{nil, context.Canceled, errCredential, &failure{class: "unauthorized"}, &failure{class: "conflict"}} {
		if _, retry := RecoveryDelay(err); retry {
			t.Fatal("terminal failure retried")
		}
	}
}
