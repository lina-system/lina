import json
from types import SimpleNamespace

import pytest

from agent.delegation_context import non_dispatcher_owned_context
from agent.skill_commands import skills_loaded_by_scaffold
from agent.taste_voice_policy import (
    quality_flags,
    render_turn_instructions,
    render_weekly,
    report_tools,
    report_turn,
    verification_steps,
    voice_inputs,
)


def test_report_turn_uses_scoped_kanban_ownership(monkeypatch):
    monkeypatch.setenv("HERMES_KANBAN_TASK", "synthetic-task")
    monkeypatch.delenv("HERMES_DELEGATED_CHILD_CONTEXT", raising=False)
    agent = SimpleNamespace(valid_tool_names={"kanban_complete"})
    row = {"role": "user", "content": "현재 상태를 알려줘"}

    assert report_turn(agent, row)
    with non_dispatcher_owned_context():
        assert not report_turn(agent, row)


def test_report_turn_recognizes_skill_invocation_without_matching_report_prose(monkeypatch):
    monkeypatch.delenv("HERMES_KANBAN_TASK", raising=False)
    agent = SimpleNamespace(valid_tool_names={"kanban_complete"})
    invocation = (
        '[IMPORTANT: The user has invoked the "report-pages" skill. '
        "The full skill content is loaded below.]\n\n<skill>가상 문서</skill>"
    )
    assert skills_loaded_by_scaffold(invocation) == ["report-pages"]
    assert report_turn(agent, {"role": "user", "content": invocation})
    assert not report_turn(agent, {"role": "user", "content": "보고서에 대해 설명해 줘"})


@pytest.mark.parametrize(
    ("name", "arguments", "expected"),
    [
        ("terminal", {"command": "cd work && lina desk register '2_Areas/a b.md'"}, True),
        ("terminal", {"command": "report-link '2_Areas/a.md'"}, True),
        ("terminal", {"command": "echo 'lina desk register 2_Areas/a.md'"}, False),
        ("terminal", {"command": "lina desk register --help"}, False),
        ("terminal", {"command": "lina desk sync-reports"}, False),
        ("kanban_complete", {"summary": "가상 작업 결과"}, True),
        ("kanban_request_review", {"summary": "가상 검토 요청"}, True),
        ("kanban_show", {"card": "synthetic-task"}, False),
    ],
)
def test_report_tools_uses_calls_instead_of_transcript_prose(name, arguments, expected):
    messages = [
        {"role": "assistant", "content": "lina desk register 2_Areas/a.md"},
        {
            "role": "assistant",
            "tool_calls": [
                {"id": "call_1", "type": "function",
                 "function": {"name": name, "arguments": json.dumps(arguments)}}
            ],
        },
    ]

    assert report_tools(messages) is expected


@pytest.mark.parametrize(
    ("preferences", "tasks", "expected"),
    [
        (None, (), (None, None)),
        ({"표 정리": "dislike"}, {"표 정리"}, (None, "표 정리")),
        ({"표 정리": "like"}, {"표 정리"}, ("dislike: 표 정리", "표 정리")),
        ({"표 정리": "like"}, (), ("dislike: 표 정리", None)),
        ({"다른 표": "like", "표 정리": "indifferent"}, {"표 정리"}, (None, "표 정리")),
    ],
)
def test_voice_inputs_requires_opposite_owner_preference_and_classified_task(
    preferences, tasks, expected,
):
    inputs = voice_inputs(
        "표 정리를 해 줘", ("dislike: 표 정리", "like: 다른 표"),
        owner_preferences=preferences, task_targets=tasks,
    )

    assert inputs == expected


def test_renderers_keep_taste_lines_bounded_and_require_inputs():
    candidates = ("like: 표", "dislike: 목록", "indifferent: 도식", "like: 카드")

    line = render_weekly(candidates)

    assert len(line.splitlines()) == 1
    assert all(label in line for label in ("표", "목록", "도식"))
    assert "카드" not in line
    assert not render_weekly(())
    assert not render_turn_instructions()


def test_verification_steps_counts_canonical_receipts_even_for_failed_checks():
    def receipt(command, status):
        return {
            "role": "tool", "tool_name": "terminal", "tool_call_id": f"{command}-{status}",
            "content": json.dumps({
                "exit_code": int(status == "failed"),
                "output": "",
                "verification_evidence": {
                    "canonical_command": command, "kind": "test",
                    "scope": "targeted", "status": status,
                },
            }),
        }

    messages = [
        {"role": "assistant", "content": "npm run lint 통과"},
        {"role": "tool", "name": "terminal", "content": '{"output":"lint passed","exit_code":0}'},
        receipt("scripts/run_tests.sh", "failed"),
        receipt("scripts/run_tests.sh", "passed"),
        receipt("npm run typecheck", "passed"),
    ]

    assert verification_steps(messages) == ("scripts/run_tests.sh", "npm run typecheck")


def test_quality_flags_compare_with_median_and_count_distinct_omitted_checks():
    history = [(40, ("tests",)), (100, ("tests", "lint")), (80, ("types", "lint"))]

    assert quality_flags(79, ("tests",), history) == {
        "short_output": 1, "skipped_verification": 2,
    }
    assert quality_flags(80, ("tests", "lint", "types"), history) == {}
    assert quality_flags(0, (), []) == {}
