// SPDX-License-Identifier: Apache-2.0

package modelpath

import "testing"

func TestLocalFromEnv(t *testing.T) {
	empty, err := LocalFromEnv(func(string) string { return "" })
	if err != nil || empty != nil {
		t.Fatalf("empty=%+v err=%v", empty, err)
	}
	values := map[string]string{"LINA_APPLEFM_HELPER": "/usr/local/bin/lina-applefm", "HOME": "/Users/person", "LANG": "ko_KR.UTF-8"}
	got, err := LocalFromEnv(func(key string) string { return values[key] })
	if err != nil || got.Helper != values["LINA_APPLEFM_HELPER"] || got.Model != onDeviceModel || got.PrivateCloud || len(got.Env) != 2 {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	values["LINA_APPLEFM_PRIVATE_CLOUD"] = "1"
	values["LINA_APPLEFM_MODEL"] = ""
	got, err = LocalFromEnv(func(key string) string { return values[key] })
	if err != nil || got.Model != privateCloudModel || !got.PrivateCloud {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	values["LINA_APPLEFM_HELPER"] = "relative"
	if _, err = LocalFromEnv(func(key string) string { return values[key] }); err == nil {
		t.Fatal("relative helper accepted")
	}
	values["LINA_APPLEFM_HELPER"] = "/usr/local/bin/lina-applefm"
	values["LINA_APPLEFM_PRIVATE_CLOUD"] = "yes"
	if _, err = LocalFromEnv(func(key string) string { return values[key] }); err == nil {
		t.Fatal("private cloud flag accepted")
	}
}
