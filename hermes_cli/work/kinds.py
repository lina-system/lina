"""The to-do kind word of the record (lina#505).

The old record renamed the to-do kind ``할 일`` to ``작업`` on 11-06 (``work migrate-names``) and kept older lines
as written, so a record holds both words. Every reader takes both as one kind through ``kind_now``; new lines say
``TODO``; ``lina work json`` prints ``JSON_TODO``, the word the dashboard and app read (the old tool's
``JSON_TASK_KIND``).
"""

from __future__ import annotations

TODO = "작업"
OLD_TODO = "할 일"
JSON_TODO = OLD_TODO


def kind_now(word):
    return TODO if word == OLD_TODO else word


def current(row):
    """*row* with its kind read as today (in place; returns the row)."""
    if isinstance(row, dict) and row.get("kind") == OLD_TODO:
        row["kind"] = TODO
    return row
