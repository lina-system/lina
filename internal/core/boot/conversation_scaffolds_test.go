// SPDX-License-Identifier: Apache-2.0

package boot

import "testing"

// These queries include provider history without displaying protocol items. Keep their
// bytes, including the different notice sets, unchanged by the extraction.
func TestConversationScaffoldSQLPreservesQueries(t *testing.T) {
	const modelContext = `SELECT id FROM conv_event WHERE conversation_id=? AND (speaker_kind IN ('person','agent','forwarded') OR scaffold_kind IN ('provider_item','interruption','failure','persona_change','run_handoff','background_notice','coding_handoff','work_completion','timer','timer_work','handoff_check','worker_session','worker_completion','question_default','question_answer'))`
	const display = `
 SELECT e.conversation_id,CASE WHEN e.scaffold_kind='result' AND e.speaker_kind='agent' AND json_valid(e.body)
        THEN 'result-card:'||json_extract(e.body,'$.result_id') ELSE e.id END AS item_key,
        e.id AS event_id,'' AS partial,'' AS turn_id,
        e.rowid AS fallback,1 AS phase,0 AS partial_position
 FROM conv_event e
 WHERE (e.speaker_kind IN ('person','agent','forwarded') OR e.scaffold_kind IN ('interruption','failure','skill_change','persona_change','worker_session','run_handoff','background_notice','worker_result','coding_handoff','timer','plan_rejection','question_default','question_answer'))
   AND coalesce(e.scaffold_kind,'')<>'provider_item'
   AND (coalesce(e.scaffold_kind,'')<>'result' OR (e.speaker_kind='agent' AND
        CASE WHEN json_valid(e.body) THEN json_type(e.body,'$.body')='object' ELSE 0 END
        AND EXISTS(SELECT 1 FROM result_heads h WHERE h.event_id=e.id)
        AND NOT EXISTS(SELECT 1 FROM plan_drafts p JOIN result_heads h ON h.id=p.result_id WHERE h.event_id=e.id)))

 UNION ALL
 SELECT e.conversation_id,h.id,e.id,'','',e.rowid,1,0
 FROM result_heads h JOIN plan_drafts p ON p.result_id=h.id JOIN conv_event e ON e.id=h.event_id
 UNION ALL
 SELECT e.conversation_id,printf('%s:%d:%d',r.id,p.round+1,p.ordinal),'',p.body,r.id,
        e.rowid,0,p.rowid
 FROM turn_runs r
 JOIN turn_control c ON c.turn_id=r.id
 JOIN conv_event e ON e.id=c.notice_event
 JOIN turn_partials p ON p.turn_id=r.id
 WHERE r.state IN ('aborted','failed')
`
	for _, tc := range []struct {
		name, got, want string
	}{
		{"model context", conversationModelContextSources, modelContext},
		{"display", conversationHistorySources, display},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Fatalf("query changed:\n got: %q\nwant: %q", tc.got, tc.want)
			}
		})
	}
}

func TestScaffoldSQLListQuotesKinds(t *testing.T) {
	if got, want := scaffoldSQLList([]string{"first", "quote'kind"}), `('first','quote''kind')`; got != want {
		t.Fatalf("SQL list = %q, want %q", got, want)
	}
}
