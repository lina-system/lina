// SPDX-License-Identifier: Apache-2.0
package clientsrv

import (
	"encoding/json"
	"slices"

	"github.com/thisisjun786/lina/kit/protocol"
)

var MocksCapability = protocol.Capability{Name: "mocks", Version: "1.0"}

// The topic ledger retains the complete newest protocol representation. Each
// connection receives only its negotiated representation, including snapshots.
func projectMocks(e protocol.Envelope, s Session) protocol.Envelope {
	version := s.ProtocolVersion
	if version == "" {
		version = "1.0"
	}
	e.Protocol.Version = version
	project := func(kind protocol.Kind, payload protocol.Payload) (protocol.Kind, protocol.Payload) {
		value, ok := payload.(protocol.Result)
		if !ok || value.Type != protocol.ResultTypeMock {
			return kind, payload
		}
		if version == "1.1" {
			if !slices.Contains(s.Capabilities, MocksCapability) {
				value.Body = nil
			}
			return kind, value
		}
		text := value.Title + "\n" + value.Summary
		if value.Text != nil {
			text += "\n" + *value.Text
		}
		speaker := protocol.ConversationSpeakerAgent
		return protocol.KindItemDone, protocol.ItemDone{Text: &text, Speaker: &speaker, Stream: value.Stream}
	}
	snapshot := func(p protocol.Snapshot) protocol.Snapshot {
		p.Items = slices.Clone(p.Items)
		for i, item := range p.Items {
			if item.Kind != protocol.KindResult {
				continue
			}
			var value protocol.Result
			if json.Unmarshal(item.Payload, &value) != nil {
				continue
			}
			kind, body := project(item.Kind, value)
			raw, err := json.Marshal(body)
			if err == nil {
				p.Items[i].Kind, p.Items[i].Payload = kind, raw
			}
		}
		return p
	}
	switch p := e.Payload.(type) {
	case protocol.Snapshot:
		e.Payload = snapshot(p)
	case protocol.History:
		if p.Snapshot != nil {
			value := snapshot(*p.Snapshot)
			p.Snapshot = &value
		}
		e.Payload = p
	default:
		e.Kind, e.Payload = project(e.Kind, e.Payload)
	}
	return e
}

func implementedClientVersion(v string) bool { return v == "1.0" || v == "1.1" }
func clientVersion(v string) string {
	if implementedClientVersion(v) {
		return v
	}
	return "1.0"
}
