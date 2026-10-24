// SPDX-License-Identifier: Apache-2.0

package proc

import (
	"reflect"
	"testing"
)

// The supervisor's own environment carries the host's race detector options
// without the detector's exit sleep, which would otherwise delay every
// command. The command's environment is sent separately and is unaffected.
func TestSupervisorEnvironmentDisablesRaceExitSleep(t *testing.T) {
	t.Setenv("GORACE", "")
	if got, want := supervisorEnvironment(), []string{"GORACE=atexit_sleep_ms=0"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("supervisor environment = %q, want %q", got, want)
	}
	t.Setenv("GORACE", "log_path=/tmp/race halt_on_error=1 atexit_sleep_ms=1500")
	if got, want := supervisorEnvironment(), []string{"GORACE=log_path=/tmp/race halt_on_error=1 atexit_sleep_ms=0"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("supervisor environment = %q, want %q", got, want)
	}
}
