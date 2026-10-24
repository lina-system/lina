// SPDX-License-Identifier: Apache-2.0
package canon

import (
	"context"
	"errors"
)

var ErrDevelopmentSchema = errors.New("development schema does not match the published migration catalog")

// Migrations returns the ordered application schema. Published versions stay
// fixed; new table groups append a migration in integration order.
func Migrations() []Migration {
	return []Migration{
		{Version: 1, SQL: schemaAssets},
		{Version: 2, SQL: InboxSchema},
		{Version: 3, SQL: authorityEffectsSchema},
		{Version: 4, SQL: authorityApprovalsSchema},
		{Version: 5, SQL: agentsSchema},
		{Version: 6, SQL: accountsSchema},
		{Version: 7, SQL: ledgerSchema},
		{Version: 8, SQL: memorySchema},
		{Version: 9, SQL: ledgerSearchSchema},
		{Version: 10, SQL: memorySearchSchema},
		{Version: 11, SQL: retractSchema + retractLineageSchema + retractViewsSchema + retractSearchSchema},
		{Version: 12, SQL: TurnSchema},
		{Version: 13, SQL: TurnControlSchema},
		{Version: 14, SQL: TurnOutcomeSchema},
		{Version: 15, SQL: compactionSchema},
		{Version: 16, SQL: AccountSignInSchema},
		{Version: 17, SQL: ChannelsSchema},
		{Version: 18, SQL: TelegramSchema},
		{Version: 19, SQL: ExchangeSchema},
		{Version: 20, SQL: conversationOrderSchema},
		{Version: 21, SQL: GrowthSchema},
		{Version: 22, SQL: MoiraiJobsSchema + BudgetSchema},
		{Version: 23, SQL: SkillsSchema},
		{Version: 24, SQL: ImportSchema + importCompletionSchema},
		{Version: 25, SQL: RunsSchema + RunningListSchema + LaneNoticeSchema},
		{Version: 26, SQL: WorkSchema},
		{Version: 27, SQL: RecordsSchema},
		{Version: 28, SQL: WorkWaitingSchema},
		{Version: 29, SQL: RecordsSearchSchema},
		{Version: 30, SQL: mergesSchema},
		{Version: 31, SQL: WorkerDispatchSchema},
		{Version: 32, SQL: DiscordSchema},
		{Version: 33, SQL: recordsKoreanSearchSchema},
		{Version: 34, SQL: WaitingDecisionsSchema},
		{Version: 35, SQL: presetEffectsSchema},
		{Version: 36, SQL: skillMaintenanceSchema},
		{Version: 37, SQL: workEngineSchema},
		{Version: 38, SQL: recordsTodoSchema},
		{Version: 39, SQL: workEventSequenceSchema},
		{Version: 40, SQL: WorkCompletionsSchema},
		{Version: 41, SQL: TimersSchema},
		{Version: 42, SQL: startersSchema},
		{Version: 43, SQL: WorkerChoiceSchema},
		{Version: 44, SQL: namingSchema},
		{Version: 45, SQL: recordOperationsSchema},
		{Version: 46, SQL: WorkSwitchSchema},
		{Version: 47, SQL: handoffsSchema},
		{Version: 48, SQL: workReviewsSchema},
		{Version: 49, SQL: ResultsSchema + PlanningSchema},
		{Version: 50, SQL: doneEvidenceSchema},
		{Version: 51, SQL: conversationQuestionsSchema},
		{Version: 52, SQL: workerRecoverySchema},
		{Version: 53, SQL: retractSearchDeltaSchema},
		{Version: 54, SQL: citeSchema},
		{Version: 55, SQL: inputBindingSchema},
		{Version: 56, SQL: MemoryExtractionSchema},
		{Version: 57, SQL: A2ASchema},
		{Version: 58, SQL: recallSchema},
		{Version: 59, SQL: steeringSchema},

		{Version: 60, SQL: recallScopeSchema},
		{Version: 61, SQL: workerMessagesSchema},
		{Version: 62, SQL: a2aSchema},
		{Version: 63, SQL: retractCiteSchema},
		{Version: 64, SQL: MiniAppsSchema},
		{Version: 65, SQL: MessengerSchema},
		{Version: 66, SQL: EventErasureSchema},
{Version: 67, SQL: ModelServedSchema},
		{Version: 68, SQL: MessengerImportSchema},
		{Version: 69, SQL: TeamsSchema},
		{Version: 70, SQL: workRunLimitSchema},
	}
}

// publishedMigrationTables records the first migration that creates each checked table.
var publishedMigrationTables = []struct {
	version int
	table   string
}{
	{59, "work_steering"}, {59, "work_steering_receipts"},
	{56, "memory_extraction_cursor"},
	{61, "worker_message_decisions"}, {61, "worker_message_budget"}, {61, "worker_message_admissions"},
	{11, "retract_meta"}, {12, "turn_runs"}, {12, "turn_items"}, {12, "turn_calls"}, {12, "turn_partials"}, {13, "turn_control"}, {14, "turn_outcome"}, {15, "sum_node"}, {15, "sum_edge"}, {15, "sum_cover"},
	{16, "owner_binding"}, {16, "sign_in_host"}, {16, "sign_in_identities"},
	{17, "channel_connections"}, {17, "channel_bindings"}, {17, "channel_actions"}, {17, "channel_receipts"}, {17, "channel_outbound"},
	{18, "telegram_buttons"}, {18, "telegram_presentations"},
	{19, "exchanges"}, {19, "exch_msg"}, {19, "exch_event"}, {19, "exch_waiting"},
	{20, "conversation_order"},
	{21, "agent_growth_steps"}, {22, "moirai_jobs"}, {22, "budget_usage"},
	{23, "skill_versions"}, {23, "skill_heads"}, {23, "skill_undone"}, {23, "skill_repo"},
	{57, "a2a_connections"}, {57, "a2a_calls"},
	{62, "a2a_tasks"}, {62, "a2a_messages"},
	{31, "worker_dispatch"}, {52, "worker_recovery"},
	{41, "timer"}, {41, "timer_fire"}, {40, "work_completion_origins"}, {43, "worker_quota"}, {40, "work_completions"}, {40, "work_completion_claims"},
	{34, "waiting_decisions"},
	{46, "work_worker_reservations"}, {46, "work_switches"}, {46, "work_switch_tasks"}, {46, "work_switch_events"},
	{49, "result_heads"}, {49, "plan_drafts"}, {49, "plan_confirmations"}, {49, "plan_rejections"},
	{28, "work_waiting_rounds"}, {28, "work_waiting_decisions"},
	{38, "records_todo"},
	{36, "skill_maintenance"}, {36, "skill_maintenance_members"}, {36, "skill_maintenance_notices"},
	{26, "work_ledger"}, {26, "work_cards"}, {26, "work_active_runs"},
	{30, "authority_merges"},
	{48, "work_reviews"},
	{32, "discord_presentations"}, {32, "discord_buttons"}, {32, "channel_steering"},
	{35, "authority_delegations"}, {35, "coding_managers"}, {35, "coding_assignments"},
	{37, "work_events"}, {37, "work_records"},
	{44, "naming_breaches"},
	{47, "exch_origins"}, {47, "exch_notices"}, {47, "artifact_changes"},
	{50, "done_completions"}, {49, "result_saves"},
	{42, "agent_starters"}, {42, "agent_selection"},
	{45, "record_operations"}, {45, "record_candidates"},
	{54, "cite"},
	{55, "records_activity"},
	{58, "recall_doc"}, {58, "recall_text"}, {58, "recall_pair"}, {58, "recall_pending"},
	{60, "recall_conversation_scope"},
	{51, "conversation_questions"}, {51, "conversation_question_resolutions"},
	{64, "mini_app"}, {64, "mini_app_revision"}, {64, "mini_app_change"}, {64, "mini_app_event"},
	{65, "messenger_source"}, {65, "messenger_chat"}, {65, "messenger_coverage"},
	{65, "messenger_record"}, {65, "messenger_record_revision"}, {65, "messenger_effect"},
	{67, "model_served"},
	{68, "messenger_import"}, {68, "messenger_legacy_ref"},
	{69, "teams_connection"}, {69, "messenger_author"},
}

// Reject incompatible pre-release layouts before opening the writer. There is
// no renumbering or repair path for development databases with a different
// catalog. Generic canon users with their own migration lists are unaffected.
func validatePublishedCatalog(ctx context.Context, path string, version int, migrations []Migration) error {
	if version == 0 || len(migrations) < 14 {
		return nil
	}
	catalog := Migrations()
	for i := 0; i < min(len(catalog), len(migrations)); i++ {
		if migrations[i] != catalog[i] {
			return nil
		}
	}
	db, close, err := openInspection(ctx, path)
	if err != nil {
		return err
	}
	defer close()
	for _, entry := range publishedMigrationTables {
		var count int
		if err = db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_schema WHERE type='table' AND name=?", entry.table).Scan(&count); err != nil {
			return err
		}
		if (count == 1) != (version >= entry.version) {
			return ErrDevelopmentSchema
		}
	}
	return nil
}
