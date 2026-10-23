// SPDX-License-Identifier: Apache-2.0

package clientsrv

import (
	"encoding/json"
	"slices"

	"github.com/thisisjun786/lina/kit/protocol"
)

var ResultsCapability = protocol.Capability{Name: "results", Version: "1.0"}

// Projection is per connection, after replay selection. Retained frames always
// contain the whole validated revision, so reconnect uses the same boundary.
func projectResults(e protocol.Envelope, session Session) protocol.Envelope {
	if session.ProtocolVersion != "" {
		e.Protocol.Version = session.ProtocolVersion
	}
	var capability *protocol.Capability
	for _, c := range session.Capabilities {
		if protocol.CapabilityIdentity(c, ResultsCapability) {
			copy := c
			capability = &copy
			break
		}
	}
	project := func(p protocol.Result) (protocol.Kind, protocol.Payload) {
		if p.Type != protocol.ResultTypeSurface {
			return protocol.KindResult, p
		}
		if session.ProtocolVersion != "1.1" || capability == nil {
			text := p.Title + "\n" + p.Summary
			if p.Text != nil {
				text += "\n" + *p.Text
			}
			return protocol.KindItemDone, protocol.ItemDone{Text: &text, Stream: p.Stream}
		}
		if capability.Types == nil || !slices.Contains(*capability.Types, protocol.ResultTypeSurface) || capability.Catalogs == nil || !slices.Contains(*capability.Catalogs, "lina.cards/1.0") {
			p.Body = nil
		}
		return protocol.KindResult, p
	}
	snapshot := func(s protocol.Snapshot) protocol.Snapshot {
		items := slices.Clone(s.Items)
		for i, item := range items {
			if item.Kind != protocol.KindResult {
				continue
			}
			var p protocol.Result
			if json.Unmarshal(item.Payload, &p) != nil {
				continue
			}
			kind, value := project(p)
			items[i].Kind = kind
			items[i].Payload, _ = json.Marshal(value)
		}
		s.Items = items
		return s
	}
	switch p := e.Payload.(type) {
	case protocol.Result:
		e.Kind, e.Payload = project(p)
	case protocol.Snapshot:
		e.Payload = snapshot(p)
	case protocol.History:
		if p.Snapshot != nil {
			value := snapshot(*p.Snapshot)
			p.Snapshot = &value
		}
		e.Payload = p
	}
	return e
}

// SurfaceResultVisible is also used by handlers that return a full body.
func SurfaceResultVisible(s Session) bool {
	return s.ProtocolVersion == "1.1" && protocol.HasCapability(s.Capabilities, ResultsCapability)
}
