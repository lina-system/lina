// SPDX-License-Identifier: Apache-2.0

package clientsrv

import (
	"context"
	"testing"

	"github.com/thisisjun786/lina/kit/protocol"
)

type kakaoStub struct {
	ended []string
}

func (s *kakaoStub) KakaoTalk(context.Context, Session, protocol.KakaotalkRequest) (Result, error) {
	return Result{}, nil
}

func (s *kakaoStub) EndKakaoTalk(connection string) {
	s.ended = append(s.ended, connection)
}

func TestKakaoTalkDisconnectCancelsTheRelay(t *testing.T) {
	stub := &kakaoStub{}
	operation, ok := KakaoTalkOperations(stub)[protocol.KindKakaotalkRequest]
	if !ok || operation.Disconnect == nil {
		t.Fatal("a client disconnect has no KakaoTalk cancellation")
	}
	operation.Disconnect(Session{ConnectionID: "connection-1"})
	if len(stub.ended) != 1 || stub.ended[0] != "connection-1" {
		t.Fatalf("ended connections: %v", stub.ended)
	}
}
