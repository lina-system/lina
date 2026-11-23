"""Owner taste forget through Heart, with disposable profile homes and PostgreSQL (#360)."""

from hashlib import sha256

import psycopg
import pytest

from hermes_cli.heart import rows, storage
from hermes_cli.heart.models import Change
from hermes_cli.heart.refiner import review
from hermes_cli.heart.taste_context import core_lines, has_tastes, pick_lines
from hermes_cli.work import env
from tests.hermes_cli.test_heart_journal import invoke, sandbox
from tests.hermes_cli.test_taste_refiner import journal, turn

__all__ = ["sandbox"]

LABEL = "수채화"


def add_taste(sql, agent, label=LABEL, status="active"):
    [(taste_id,)] = sql(
        "INSERT INTO memory.tastes(agent_id,target_kind,label,status,valence,strength,origin) "
        "VALUES (%s,'method',%s,%s,'like',0.6,'outcome') RETURNING id",
        (agent, label, status),
    )
    return taste_id


@pytest.mark.parametrize("status", ["active", "core"])
def test_owner_in_beta_forgets_only_alpha_and_removes_prompt_and_undo_copies(
    sandbox, memory_store_sql, monkeypatch, status
):
    from hermes_cli.heart.forget import forget_from_conversation

    sql = memory_store_sql
    alpha, beta = sandbox["alpha"], sandbox["beta"]
    own_id = add_taste(sql, "alpha", status=status)
    add_taste(sql, "beta")
    note = alpha / "SOUL.self.md"
    backup = alpha / "heart/self-backups/2025-06-01T120000+0000.md"
    changes = alpha / "heart/changes.jsonl"
    unrelated = "Keep the sidebar compact.\n"
    forgotten_line = "수채화를 즐긴다. (2025-06-01, 근거 2025-06-01T12:00:00+00:00)"
    prior = forgotten_line + "\n" + unrelated
    backup.parent.mkdir(parents=True)
    note.write_text(prior, encoding="utf-8")
    backup.write_text(prior, encoding="utf-8")
    storage.append(changes, Change(ts="2025-06-01T12:00:00+00:00", added=(forgotten_line, unrelated.strip())))
    (beta / note.name).write_text(prior, encoding="utf-8")
    monkeypatch.setenv("HERMES_HOME", str(beta))
    env.current().locale = "ko"
    read_alpha = pick_lines(alpha, LABEL) if status == "active" else core_lines(alpha)
    assert read_alpha == (f"like: {LABEL}",)

    result = forget_from_conversation(
        "그건 잊어", owner_verified=True, history=("수채화를 좋아해.",), target_home=alpha,
    )

    assert result.forgotten.taste_id == own_id
    assert result.forgotten.label == LABEL
    assert LABEL not in result.response and any("가" <= ch <= "힣" for ch in result.response)
    assert sql(
        "SELECT status, label, label_hash, forgotten_by, forgotten_on = CURRENT_DATE "
        "FROM memory.tastes WHERE id=%s", (own_id,),
    ) == [("forgotten", None, sha256(LABEL.encode()).digest(), "owner", True)]
    assert pick_lines(alpha, LABEL) == core_lines(alpha) == ()
    assert not has_tastes(alpha)
    assert pick_lines(beta, LABEL) == (f"like: {LABEL}",)
    assert (beta / note.name).read_text(encoding="utf-8") == prior
    assert note.read_text(encoding="utf-8") == backup.read_text(encoding="utf-8") == unrelated
    assert storage.records(changes, Change)[0].added == (unrelated.strip(),)
    monkeypatch.setenv("HERMES_HOME", str(alpha))
    assert invoke("undo")[0] == 0
    assert note.read_text(encoding="utf-8") == unrelated


def test_forget_retires_sole_journal_keeps_shared_evidence_and_blocks_replay(sandbox, memory_store_sql):
    from hermes_cli.heart.forget import forget_from_conversation

    sql = memory_store_sql
    home = sandbox["alpha"]
    [(today,)] = sql("SELECT CURRENT_DATE")
    turn(sql, day=today)
    sql("UPDATE memory.messages SET content='수채화를 좋아해. 전시는 금요일.' WHERE uid='turn'")
    journal(home, today, "like", target=LABEL)
    journal(home, today, "dislike", target=LABEL)
    [(sole,), (shared,)] = sql("SELECT uid FROM memory.messages WHERE source='heart' ORDER BY message_id")
    target_id = add_taste(sql, "alpha")
    surviving_id = add_taste(sql, "alpha", "oil painting", "core")
    for taste_id, uid in ((target_id, sole), (target_id, shared), (surviving_id, shared)):
        sql(
            "INSERT INTO memory.taste_evidence(agent_id,taste_id,journal_uid,message_uids,observed_on,route,sign,weight) "
            "VALUES ('alpha',%s,%s,ARRAY['turn'],%s,'outcome',1,0.225)",
            (taste_id, uid, today),
        )
    before = sql("SELECT * FROM memory.messages WHERE uid='turn'")

    result = forget_from_conversation(
        "수채화 취향은 잊어", owner_verified=True, history=(), target_home=home,
    )

    assert set(result.forgotten.journal_uids) == {sole, shared}
    assert sql("SELECT uid, forgotten_at IS NOT NULL FROM memory.messages WHERE source='heart' ORDER BY message_id") == [
        (sole, True), (shared, False),
    ]
    assert sql("SELECT taste_id, journal_uid FROM memory.taste_evidence") == [(surviving_id, shared)]
    assert sql("SELECT * FROM memory.messages WHERE uid='turn'") == before
    assert sql("SELECT reason, changed_by FROM memory.taste_events WHERE taste_id=%s", (target_id,)) == [
        ("forgotten", "owner"),
    ]
    with rows.open_store(home) as store:
        assert review(store, today)["reserved-target"] == 1
    assert sql("SELECT id FROM memory.tastes WHERE label=%s", (LABEL,)) == []


def test_untrusted_request_and_direct_beta_write_cannot_forget_alpha(sandbox, memory_store_sql):
    from hermes_cli.heart.forget import forget_from_conversation

    taste_id = add_taste(memory_store_sql, "alpha")
    result = forget_from_conversation(
        "수채화 취향은 잊어", owner_verified=False, history=(), target_home=sandbox["alpha"],
    )
    assert result.forgotten is None
    with rows.open_store(sandbox["beta"]) as beta, beta.write(), beta._writer_cursor() as cur:
        cur.execute(
            "UPDATE memory.tastes SET status='forgotten', forgotten_by='owner' "
            "WHERE agent_id='alpha' AND id=%s RETURNING id", (taste_id,),
        )
        assert cur.fetchall() == []
    with rows.open_store(sandbox["beta"]) as beta, pytest.raises(psycopg.errors.InsufficientPrivilege):
        with beta.write(), beta._writer_cursor() as cur:
            cur.execute(
                "INSERT INTO memory.tastes(agent_id,target_kind,label,status,valence,strength,origin) "
                "VALUES ('alpha','method','foreign write','active','like',0.6,'outcome')",
            )
    assert pick_lines(sandbox["alpha"], LABEL) == (f"like: {LABEL}",)


@pytest.mark.parametrize(
    ("utterance", "history", "target", "recognized"),
    [
        ("수채화 취향은 잊지 마", (), "alpha", False),
        ("수채화 취향은 잊는 게 좋을까?", (), "alpha", False),
        ("그건 잊어", ("수채화와 유화를 좋아해.",), "alpha", True),
        ("수채화 취향은 잊어", (), None, True),
    ],
)
def test_forget_does_not_guess_intent_or_target(sandbox, memory_store_sql, utterance, history, target, recognized):
    from hermes_cli.heart.forget import forget_from_conversation

    add_taste(memory_store_sql, "alpha")
    add_taste(memory_store_sql, "alpha", "유화")
    result = forget_from_conversation(
        utterance, owner_verified=True, history=history, target_home=sandbox[target] if target else None,
    )

    assert (result is not None) == recognized
    if result is not None:
        assert result.forgotten is None
    assert pick_lines(sandbox["alpha"], "수채화와 유화") == ("like: 수채화", "like: 유화")
