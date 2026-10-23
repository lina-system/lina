// SPDX-License-Identifier: Apache-2.0

package proc

import (
	"reflect"
	"testing"
)

func TestTrackDescendantsFollowsRecordedParent(t *testing.T) {
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
	tracked := map[uint64]struct{}{owner: {}}
	want := []processIdentity{
		{pid: 200, uniqueID: 20, parentUniqueID: owner},
		{pid: 300, uniqueID: 30, parentUniqueID: 20},
	}
	if got := trackDescendants(tracked, all); !reflect.DeepEqual(got, want) {
		t.Fatalf("descendants = %+v, want %+v", got, want)
	}
	if _, ok := tracked[40]; ok {
		t.Fatal("unrelated process was tracked")
	}
}

// The identity of a discovered parent is kept, so its children stay attributable
// after the kernel has reaped it and its own record is gone.
func TestTrackDescendantsRetainsReapedParent(t *testing.T) {
	const owner = 10
	tracked := map[uint64]struct{}{owner: {}}
	trackDescendants(tracked, []processIdentity{{pid: 200, uniqueID: 20, parentUniqueID: owner}})
	got := trackDescendants(tracked, []processIdentity{{pid: 300, uniqueID: 30, parentUniqueID: 20}})
	want := []processIdentity{{pid: 300, uniqueID: 30, parentUniqueID: 20}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("descendants after the parent was reaped = %+v, want %+v", got, want)
	}
}

func TestTrackDescendantsExcludeUnrelatedAndUnknown(t *testing.T) {
	const owner = 10
	all := []processIdentity{
		{pid: 100, uniqueID: 1, parentUniqueID: 1},   // self-parented
		{pid: 101, uniqueID: 11, parentUniqueID: 11}, // self-parented
		{pid: 102, uniqueID: 0},                      // unreadable identity
		{pid: 103, uniqueID: 12, parentUniqueID: 99}, // parent not in the table
	}
	tracked := map[uint64]struct{}{owner: {}}
	if got := trackDescendants(tracked, all); len(got) != 0 {
		t.Fatalf("descendants = %+v, want none", got)
	}
	if _, ok := tracked[0]; ok {
		t.Fatal("unreadable identity was tracked")
	}
}
