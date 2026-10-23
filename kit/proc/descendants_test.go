// SPDX-License-Identifier: Apache-2.0

package proc

import (
	"reflect"
	"testing"
)

func TestDescendantIdentitiesFollowRecordedParent(t *testing.T) {
	const owner = 10
	all := []processIdentity{
		// The leaf is listed before its parent, and its intermediate parent
		// (pid 201, unique id 20) has already exited: the process table now
		// reports launchd as the leaf's parent, but the recorded identity
		// still names the intermediate process.
		{pid: 300, uniqueID: 30, parentUniqueID: 20},
		{pid: 200, uniqueID: 20, parentUniqueID: owner},
		{pid: 400, uniqueID: 40, parentUniqueID: 1},
		{pid: 500, uniqueID: 50, parentUniqueID: 40},
	}
	want := []processIdentity{
		{pid: 200, uniqueID: 20, parentUniqueID: owner},
		{pid: 300, uniqueID: 30, parentUniqueID: 20},
	}
	if got := descendantIdentities(owner, all); !reflect.DeepEqual(got, want) {
		t.Fatalf("descendants = %+v, want %+v", got, want)
	}
}

func TestDescendantIdentitiesExcludeUnrelatedAndUnknown(t *testing.T) {
	const owner = 10
	all := []processIdentity{
		{pid: 100, uniqueID: 1, parentUniqueID: 1},   // self-parented
		{pid: 101, uniqueID: 11, parentUniqueID: 11}, // self-parented
		{pid: 102, uniqueID: 0},                      // unreadable identity
		{pid: 103, uniqueID: 12, parentUniqueID: 99}, // parent not in the table
	}
	if got := descendantIdentities(owner, all); len(got) != 0 {
		t.Fatalf("descendants = %+v, want none", got)
	}
	if got := descendantIdentities(0, all); got != nil {
		t.Fatalf("unknown root descendants = %+v, want nil", got)
	}
}
