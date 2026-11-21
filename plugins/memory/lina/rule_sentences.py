"""Interpret a stored owner's instruction or fact correction.

An unsupported clause invalidates the whole turn, including any otherwise
valid correction. The caller verifies authorship.
"""

from __future__ import annotations

import re
from dataclasses import dataclass, replace
from typing import Final, Literal

from hermes_cli.profiles import normalize_profile_name


@dataclass(frozen=True, slots=True)
class Interpretation:
    scope: Literal["all", "agent", "place"] | None = None
    target: str = "*"
    place_ref: str | None = None
    statement: str = ""
    fact: str | None = None
    unclear: bool = False
    refusal: str | None = None


_ID: Final = r"[A-Za-z0-9가-힣][A-Za-z0-9가-힣_-]{0,63}"
NAMED_PLACE_REF: Final = rf"(?:channel|source|goal):{_ID}"
_SEP: Final = r"(?: *, *| +)"
_ALL: Final = re.compile(
    r"(?:everyone|all of you|(?:all|every) agents?|모두|다들|모든 에이전트(?:는|에게)?)"
    + _SEP, re.IGNORECASE,
)
_YOU: Final = re.compile(r"(?:you(?: should| must)?|너는|너만|너한테는|당신은)" + _SEP, re.IGNORECASE)
_NAMED_AGENT: Final = re.compile(
    rf"(?:tell (?:the )?(?P<told>{_ID}) agent (?:to |(?=never to ))|agent[: ](?P<id>{_ID}){_SEP}|"
    rf"(?P<ko>{_ID}) 에이전트(?:는|만){_SEP})", re.IGNORECASE,
)
_PLACE: Final = re.compile(
    rf"(?:(?:in|on|for) (?:this (?P<current>room|channel|goal|source|mail account)|"
    rf"that (?P<referenced>mail account)|"
    rf"the (?P<name>{_ID}) (?P<kind>room|channel|goal|source|mail account))|"
    rf"(?:이 (?P<here>방|채널|목표|소스)|(?P<ko_name>{_ID}) (?P<ko_kind>방|채널|목표|소스))에서(?:는|만)?|"
    # Match the Korean locative particle before consuming it as part of the ID.
    rf"(?:(?:in|on|for) )?(?P<ref>{NAMED_PLACE_REF}?)(?:에서(?:는|만)?)?)"
    + _SEP, re.IGNORECASE,
)
_KINDS: Final = {
    "방": "channel", "채널": "channel", "room": "channel", "channel": "channel",
    "목표": "goal", "goal": "goal", "소스": "source", "source": "source", "mail account": "source",
}
_UNRESOLVED_NAMES: Final = frozenset(
    {"other", "that", "this", "current", "other-agent", "그", "다른", "저", "이"}
)

# Match the complete clause. Scope words and imperative endings alone do not
# establish a rule, and an allowed prefix must not hide an unsupported tail.
_EN_RULE: Final = re.compile(
    r"(?:(?:from now on,? |always |please )*(?:"
    r"(?:keep|make) (?:your |the )?(?:replies|reports|answers|summaries|updates) (?:short|brief|concise)|"
    r"(?:use|write) (?:short (?:reports|replies|sentences)|(?:one|two|three|[1-3]) lines?|"
    r"bullet points|plain (?:language|English))|"
    r"(?:answer|reply|write|report) (?:briefly|concisely|in (?:English|Korean|plain language))|"
    r"(?:lead|start) with (?:the )?(?:conclusion|answer|result)|"
    r"no (?:flourishes|emojis|jargon)|(?:omit|avoid) (?:flourishes|emojis|jargon)|"
    r"never to use (?:emojis|jargon)|"
    r"only (?:what mentions me|(?:show|send|report) (?:me )?messages that mention me)|"
    r"never tell me to (?:get approval|go ask the other side)|"
    r"don['’]t write it out like an AI))", re.IGNORECASE,
)
_KO_RULE: Final = re.compile(
    r"(?:(?:앞으로(?:는)?|항상|늘|꼭) )*(?:"
    r"(?:(?:답변|답|보고서|보고)(?:을|를|는|은)? )?(?:짧게|간단히|간결하게|존댓말로|한국어로|영어로) "
    r"(?:답(?:변)?해|써|말해|보고해)|"
    r"(?:결론|결과|요점)부터 (?:써|말해|답해|알려)|"
    r"(?:나를|나) 언급한 (?:메시지|글)만 (?:알려|보여)|"
    r"(?:미사여구|이모지|군더더기)(?:는|를)? (?:빼|쓰지 마)|"
    r"나한테 승인받아 오라고 하지 마)(?: ?(?:줘|주세요))?"
)

_CORRECTION: Final = re.compile(
    r"^(?:that(?:['’]s| is| was) wrong|correction|actually|그건 틀렸어|그게 아니야|정정할게|아니)"
    r"\s*[,.:;]\s*", re.IGNORECASE,
)
_AMENDMENT: Final = re.compile(
    r"(?:one|two|three|[1-3]) messages? (?:is|are) fine|"
    r"메시지 (?:한|두|세|[1-3]) ?개면 (?:돼|돼요|괜찮아|괜찮아요)",
    re.IGNORECASE,
)
_VALUE: Final = (
    r"(?:[A-Za-z가-힣]|Monday|Tuesday|Wednesday|Thursday|Friday|Saturday|Sunday|"
    r"[월화수목금토일]요일|\d{1,4}(?:[-:]\d{1,2}){0,2})"
)
_FACT: Final = re.compile(
    rf"(?:(?:it|(?:the |our )?(?:meeting|deadline|date|time)) (?:is|was) (?:on |at )?{_VALUE}|"
    rf"(?:회의|마감|날짜|시간|그거)(?:은|는|가|이) {_VALUE}(?:이야|야|예요|이에요))", re.IGNORECASE,
)
_CLAUSE_BOUNDARY: Final = re.compile(r"\s*;\s*|[.!] +|, (?:and|그리고) ", re.IGNORECASE)

_UNKNOWN: Final = "이 문장에서 저장할 규칙이나 사실 정정을 확인할 수 없어요."
_PLACE_UNKNOWN: Final = "어느 장소를 가리키는지 확인할 수 없어요."
_RISK_REASONS: Final = (
    (r"비밀|비밀번호|인증 정보|api[ _-]?key|password|credential|secret",
     "비밀 정보는 기록하거나 공개할 수 없어요."),
    (r"승인된 계획|내린 결정|내 결정|approved plan|my decision",
     "이미 내린 결정을 규칙으로 뒤집을 수 없어요."),
    (r"끝났다고|했다고 해|거짓|꾸며|it's done|it’s done|pretend|fabricate|lie about",
     "확인하지 않은 일을 사실처럼 보고할 수 없어요."),
    (r"승인|허락|묻지|approval|permission|before delet|without asking",
     "승인이 필요한 절차는 건너뛸 수 없어요."),
)


def _refusal_reason(text: str) -> str:
    for pattern, reason in _RISK_REASONS:
        if re.search(pattern, text, re.IGNORECASE):
            return reason
    return _UNKNOWN


def _place_ref(match: re.Match[str], place: str | None) -> str | None:
    if ref := match["ref"]:
        kind, key = ref.split(":", 1)
        return f"{kind.lower()}:{key}"
    kind = _KINDS[
        (match["current"] or match["referenced"] or match["here"] or match["kind"] or match["ko_kind"]).lower()
    ]
    if name := match["name"] or match["ko_name"]:
        return None if name.lower() in _UNRESOLVED_NAMES else f"{kind}:{name}"
    if place and re.fullmatch(rf"{kind}:[\w:.-]+", place):
        return place
    return None


def _rule(clause: str, current: str, place: str | None) -> Interpretation:
    target = current
    location = None
    place_match = _PLACE.match(clause)
    if place_match:
        location = _place_ref(place_match, place)
        if not location:
            return Interpretation(target=current, refusal=_PLACE_UNKNOWN)
        clause = clause[place_match.end():]
        target = "*"

    audience = _ALL.match(clause) or _YOU.match(clause) or _NAMED_AGENT.match(clause)
    if audience:
        if audience.re is _ALL:
            target = "*"
        elif audience.re is _YOU:
            target = current
        else:
            name = normalize_profile_name(audience["told"] or audience["id"] or audience["ko"])
            if name in _UNRESOLVED_NAMES:
                return Interpretation(target=current, refusal=_UNKNOWN)
            target = f"agent:{name}"
        clause = clause[audience.end():]

    if not (_EN_RULE.fullmatch(clause) or _KO_RULE.fullmatch(clause)):
        return Interpretation(target=current, refusal=_refusal_reason(clause))
    scope: Literal["all", "agent", "place"] = "agent"
    if location:
        scope = "place"
    elif target == "*":
        scope = "all"
    return Interpretation(
        scope=scope, target=target, place_ref=location, statement=clause,
        unclear=not (place_match or audience),
    )


def interpret(text: str, *, agent: str, place: str | None) -> Interpretation:
    """Read at most one rule and one corrected fact, preserving the owner's clause text."""
    profile = agent.removeprefix("agent:")
    if not re.fullmatch(_ID, profile):
        return Interpretation(refusal="현재 에이전트를 확인할 수 없어요.")
    current = f"agent:{profile}"
    if len(text) > 1024 or re.search(r'[\r\n`<>“”"「」]', text):
        return Interpretation(target=current, refusal="인용문이나 붙여 넣은 글은 규칙으로 저장할 수 없어요.")

    rest = re.sub(r"[.!]$", "", text.strip()).rstrip()
    rule = None
    fact = None
    while rest:
        # Consume the correction marker first, since it can itself end in a period.
        correction = _CORRECTION.match(rest)
        if correction:
            rest = rest[correction.end():]
        parts = _CLAUSE_BOUNDARY.split(rest, maxsplit=1)
        clause = parts[0].strip()
        if correction and not fact and _FACT.fullmatch(clause):
            fact = clause
        elif correction and not rule and _AMENDMENT.fullmatch(clause):
            rule = Interpretation(scope="agent", target=current, statement=clause, unclear=True)
        elif not correction and not rule:
            rule = _rule(clause, current, place)
            if rule.refusal:
                return rule
        else:
            return Interpretation(target=current, refusal=_refusal_reason(clause))
        if len(parts) == 2 and not parts[1]:
            return Interpretation(target=current, refusal=_UNKNOWN)
        rest = parts[1] if len(parts) == 2 else ""

    if not rule and not fact:
        return Interpretation(target=current, refusal=_UNKNOWN)
    return replace(rule or Interpretation(target=current), fact=fact)
