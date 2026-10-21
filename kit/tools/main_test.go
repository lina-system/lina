// SPDX-License-Identifier: Apache-2.0
package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestMainRunsOneExplicitFileRequest(t *testing.T) {
	_, root, versions := fileStore(t)
	var out, diagnostic bytes.Buffer
	code := Main(context.Background(), []string{"--root", root, "--versions", versions}, strings.NewReader(`{"operation":"write","path":"file","revision":"absent","content":"eA=="}`), &out, &diagnostic)
	var reply FileReply
	if err := json.Unmarshal(out.Bytes(), &reply); err != nil || code != 0 || reply.State != Applied || diagnostic.Len() != 0 {
		t.Fatalf("code=%d reply=%+v error=%v diagnostic=%s", code, reply, err, &diagnostic)
	}
	for _, args := range [][]string{nil, {"--root", root}, {"--root", root, "--versions", versions, "extra"}, {"--root", "relative", "--versions", versions}, {"--unknown"}} {
		out.Reset()
		diagnostic.Reset()
		if code := Main(context.Background(), args, strings.NewReader("{}"), &out, &diagnostic); code != 64 || out.Len() != 0 || diagnostic.Len() == 0 {
			t.Fatalf("usage: %d %q %q", code, &out, &diagnostic)
		}
	}
}
