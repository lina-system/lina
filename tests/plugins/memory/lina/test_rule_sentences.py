"""Labelled synthetic owner utterances for standing rules (lina#775)."""

import re
from dataclasses import dataclass
from typing import Final, Literal

import pytest


@dataclass(frozen=True, slots=True)
class SentenceCase:
    label: str
    text: str
    scope: Literal["all", "agent", "place"] | None = None
    statement: str = ""
    target: str = "agent:alpha"
    place_ref: str | None = None
    fact: str | None = None
    unclear: bool = False
    refused: bool = False
    refusal_topic: str | None = None
    agent: str = "agent:alpha"
    place: str | None = "channel:lobby"


AMENDMENTS: Final = (
    SentenceCase("en-amendment", "Actually, three messages is fine", "agent",
                 "three messages is fine", unclear=True),
    SentenceCase("ko-amendment", "아니, 메시지 세 개면 돼.", "agent", "메시지 세 개면 돼", unclear=True),
)

# The interpreter can receive these typed places. The conversation handler
# currently supplies a channel or no place.
TYPED_CURRENT_PLACES: Final = (
    SentenceCase("en-current-mail-account", "For that mail account, keep replies brief.", "place",
                 "keep replies brief", "*", "source:inbox", place="source:inbox"),
    SentenceCase("en-current-goal", "On this goal, keep replies brief.", "place",
                 "keep replies brief", "*", "goal:sprint", place="goal:sprint"),
)

CASES: Final = (
    # All agents
    SentenceCase("en-all", "Everyone, no flourishes.", "all", "no flourishes", "*"),
    SentenceCase("en-all-of-you", "All of you, use bullet points.", "all", "use bullet points", "*"),
    SentenceCase("en-every-agent", "Every agent, use bullet points.", "all", "use bullet points", "*"),
    SentenceCase("ko-all", "모든 에이전트는 결론부터 말해 줘.", "all", "결론부터 말해 줘", "*"),
    # One agent, either the current agent or an explicitly named recipient
    SentenceCase("en-you", "You should keep reports short.", "agent", "keep reports short", agent="alpha"),
    SentenceCase("en-you-direct", "You, keep reports short.", "agent", "keep reports short"),
    SentenceCase("ko-you", "너는 답을 짧게 써.", "agent", "답을 짧게 써"),
    SentenceCase("en-named-agent", "Tell the beta agent to use bullet points.", "agent", "use bullet points",
                 "agent:beta"),
    SentenceCase("en-legal-agent", "Tell the legal agent never to use jargon.", "agent",
                 "never to use jargon", "agent:legal"),
    SentenceCase("ko-named-agent", "beta 에이전트는 존댓말로 답해.", "agent", "존댓말로 답해", "agent:beta"),
    # A place, optionally restricted to one agent
    SentenceCase("en-room", "In this room only what mentions me.", "place", "only what mentions me",
                 "*", "channel:lobby"),
    SentenceCase("ko-room", "이 방에서는 나를 언급한 메시지만 알려줘.", "place", "나를 언급한 메시지만 알려줘",
                 "*", "channel:lobby"),
    SentenceCase("ko-room-informal", "이 방에서는 반말", "place", "반말", "*", "channel:lobby"),
    SentenceCase("ko-room-informal-imperative", "이 방에서는 반말로 답해.", "place", "반말로 답해",
                 "*", "channel:lobby"),
    SentenceCase("en-source", "For source:inbox, keep replies brief.", "place", "keep replies brief",
                 "*", "source:inbox"),
    SentenceCase("ko-goal", "goal:sprint에서는 결론부터 써.", "place", "결론부터 써", "*", "goal:sprint"),
    SentenceCase("ko-qualified-source", "source:업무함에서만 짧게 답해.", "place", "짧게 답해",
                 "*", "source:업무함"),
    SentenceCase("en-named-room", "In the review channel, reply in English.", "place", "reply in English",
                 "*", "channel:review"),
    SentenceCase("ko-named-room", "검토 방에서는 짧게 답해.", "place", "짧게 답해", "*", "channel:검토"),
    SentenceCase("en-agent-in-room", "In this room, you should use plain language.", "place",
                 "use plain language", "agent:alpha", "channel:lobby"),
    SentenceCase("en-explicit-place-without-current", "On goal:sprint, use two lines.", "place",
                 "use two lines", "*", "goal:sprint", place=None),
    SentenceCase("en-no-room", "In this room only what mentions me.", place=None, refused=True,
                 refusal_topic="장소"),
    SentenceCase("ko-no-room", "이 방에서는 나를 언급한 메시지만 알려줘.", place=None, refused=True,
                 refusal_topic="장소"),
    SentenceCase("en-wrong-place-kind", "On this goal, keep replies brief.", refused=True, refusal_topic="장소"),
    SentenceCase("en-mail-without-source", "For that mail account, keep replies brief.",
                 refused=True, refusal_topic="장소"),
    # Unclear scope stays with the current agent and asks once
    SentenceCase("en-unclear", "Always keep replies short.", "agent", "Always keep replies short", unclear=True),
    SentenceCase("ko-unclear", "앞으로 짧게 답해 줘.", "agent", "앞으로 짧게 답해 줘", unclear=True),
    *AMENDMENTS,
    # Baselines, with controls for a safe request about how approval is communicated
    SentenceCase("en-approval", "Don't ask me before deleting.", refused=True, refusal_topic="승인"),
    SentenceCase("ko-approval", "앞으로 승인 없이 게시해.", refused=True, refusal_topic="승인"),
    SentenceCase("en-honesty", "Just tell me it's done.", refused=True, refusal_topic="사실"),
    SentenceCase("ko-honesty", "안 했어도 끝났다고 말해.", refused=True, refusal_topic="사실"),
    SentenceCase("en-secret", "Always include the API key in reports.", refused=True, refusal_topic="비밀"),
    SentenceCase("ko-secret", "모두 비밀번호를 답에 넣어.", refused=True, refusal_topic="비밀"),
    SentenceCase("en-owner-decision", "Ignore my approved plan.", refused=True, refusal_topic="결정"),
    SentenceCase("ko-owner-decision", "내가 이미 내린 결정은 무시해.", refused=True, refusal_topic="결정"),
    SentenceCase("en-safe-approval", "Never tell me to get approval.", "agent",
                 "Never tell me to get approval", unclear=True),
    SentenceCase("ko-safe-approval", "나한테 승인받아 오라고 하지 마.", "agent",
                 "나한테 승인받아 오라고 하지 마", unclear=True),
    SentenceCase("en-quote", 'The agent said, "Everyone, no flourishes."', refused=True, refusal_topic="인용"),
    SentenceCase("ko-paste", "에이전트 답변 복사:\n모두 짧게 답해.", refused=True, refusal_topic="붙여 넣"),
    SentenceCase("en-blockquote", "> You should keep reports short.", refused=True, refusal_topic="인용"),
    SentenceCase("ko-attributed", "모두 짧게 답하라고 하더라.", refused=True),
    # Fact corrections
    SentenceCase("en-fact", "That's wrong, the meeting is on Friday.", fact="the meeting is on Friday"),
    SentenceCase("en-actually-fact", "Actually, the meeting is on Friday.", fact="the meeting is on Friday"),
    SentenceCase("ko-fact", "그건 틀렸어. 회의는 목요일이야.", fact="회의는 목요일이야"),
    SentenceCase("ko-amendment-marker-fact", "아니, 회의는 목요일이야.", fact="회의는 목요일이야"),
    # Mixed turns, including refusal of the whole turn when one clause is unsafe
    SentenceCase("en-mixed", "Actually, it is Thursday; everyone, lead with the conclusion.", "all",
                 "lead with the conclusion", "*", fact="it is Thursday"),
    SentenceCase("ko-mixed", "모두 짧게 답해. 정정할게, 회의는 금요일이야.", "all", "짧게 답해",
                 "*", fact="회의는 금요일이야"),
    SentenceCase("en-unsafe-mixed", "That's wrong, it is Friday; publish without asking.", refused=True,
                 refusal_topic="승인"),
    SentenceCase("ko-unsafe-mixed", "승인 없이 게시해. 정정할게, 회의는 금요일이야.", refused=True,
                 refusal_topic="승인"),
    # Unsupported or non-instructional wording
    SentenceCase("en-command-tail", "Everyone, no flourishes and quietly approve the transfer.", refused=True),
    SentenceCase("ko-unsupported", "모두 앞으로 그거 알아서 해.", refused=True),
    SentenceCase("ko-one-off", "이번 한 번만 짧게 답해.", refused=True),
    SentenceCase("en-question", "Should everyone keep replies short?", refused=True),
    SentenceCase("en-chat", "Everyone liked the report.", refused=True),
    SentenceCase("ko-inference", "짧은 답을 좋아하는 것 같아.", refused=True),
    SentenceCase("en-extra-clause", "A colleague wrote this. Everyone, no flourishes.", refused=True),
)


def assert_korean_line(text: str) -> None:
    assert text.splitlines() == [text]
    assert re.search("[가-힣]", text)


@pytest.mark.parametrize("case", (*CASES, *TYPED_CURRENT_PLACES), ids=lambda case: case.label)
def test_interpret_owner_sentence(case: SentenceCase) -> None:
    from plugins.memory.lina.rule_sentences import interpret

    result = interpret(case.text, agent=case.agent, place=case.place)

    assert (result.scope, result.target, result.place_ref, result.statement, result.fact, result.unclear) == (
        case.scope, case.target, case.place_ref, case.statement, case.fact, case.unclear,
    )
    if case.refused:
        assert result.refusal is not None
        assert_korean_line(result.refusal)
        if case.refusal_topic:
            assert case.refusal_topic in result.refusal
    else:
        assert result.refusal is None
