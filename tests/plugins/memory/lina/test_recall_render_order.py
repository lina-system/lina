"""Rendering keeps original evidence before prior answers without unquoting external data."""

import pytest

from plugins.memory.lina import grade


@pytest.mark.parametrize("original_grade", ["owner", "external"])
def test_original_precedes_answer_in_rendered_recall(original_grade):
    answer = {"ref": "msg:answer", "speaker": "agent", "grade": "agent",
              "excerpt": "The hub is a herb garden."}
    original = {"ref": "msg:original", "speaker": "owner" if original_grade == "owner" else "outside",
                "grade": original_grade, "excerpt": "The hub routes requests to servers."}

    rendered = grade.recall_text([answer, original])

    assert rendered.index("msg:original") < rendered.index("msg:answer")
    assert "you said before; not evidence" in rendered
    if original_grade == "external":
        assert rendered.count(grade.DATA_OPEN) == rendered.count(grade.DATA_CLOSE) == 1
        assert grade.QUOTE + original["excerpt"] in rendered
        assert rendered.index("msg:original") < rendered.index(grade.DATA_CLOSE) < rendered.index("msg:answer")
    else:
        assert grade.DATA_OPEN not in rendered
