"""The Korean writing rule as a core system-prompt section (lina#380).

It rides the frozen plugin-section slot beside the self note (``agent/system_prompt.py``
``_frozen_plugin_prompt_sections``), so it is the same bytes on every turn of a session. Only an agent whose
language setting (``display.language``, ``agent.i18n.get_language``) is Korean carries it; every other agent's
prompt has no extra byte. The owner's words for the concepts of a turn ride found memory instead
(``plugins/memory/lina/korean.py``).
"""

from __future__ import annotations

KOREAN_RULE_SECTION_ID = "agent-korean-writing"
KOREAN_RULE = (
    "한국어로 쓸 때:\n"
    "- 주인이 쓰는 말을 쓴다. 주인이 이름 붙이지 않은 개념에는 새 이름을 만들지 말고 쉬운 말로 풀어 쓴다.\n"
    "- 주인과 나누던 말투를 지킨다.\n"
    "- 영어 문장을 옮긴 듯한 번역투를 쓰지 않는다."
)


def korean_agent() -> bool:
    from agent.i18n import get_language
    return get_language() == "ko"


def render_korean_rule_section() -> tuple:
    """The rule's one section for a Korean agent, else ``()``."""
    if not korean_agent():
        return ()
    from hermes_cli.plugins import RenderedPluginSystemPromptSection
    return (RenderedPluginSystemPromptSection(
        id=KOREAN_RULE_SECTION_ID, content=KOREAN_RULE, position="after_memory", plugin="core"),)
