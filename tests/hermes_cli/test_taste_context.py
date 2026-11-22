"""Prompt reads from an agent's disposable taste store."""


def _insert(sql, label, status="active", *, agent="alpha", strength=0.6):
    return sql(
        "INSERT INTO memory.tastes(agent_id,target_kind,label,status,valence,strength,origin,"
        "protected_passed,protected_at) VALUES (%s,'method',%s,%s,'like',%s,'outcome',true,now()) RETURNING id",
        (agent, label, status, strength),
    )[0][0]


def test_pick_matches_the_turn_before_limiting_to_three_of_this_agents_active_tastes(tmp_path, memory_store_sql):
    from hermes_cli.heart.taste_context import has_tastes, pick_lines

    home = tmp_path / "profiles" / "alpha"
    assert not has_tastes(home)
    for status in ("core", "candidate", "dormant"):
        _insert(memory_store_sql, "garden " + status, status)
    _insert(memory_store_sql, "garden foreign", agent="beta")
    _insert(memory_store_sql, "unrelated", strength=1)
    _insert(memory_store_sql, "art", strength=1)
    for i, label in enumerate(("trellis", "rose bed", "raised beds", "garden soil")):
        _insert(memory_store_sql, label, strength=0.4 + i / 10)
    assert has_tastes(home)
    query = "A TRELLIS chart: raised beds, rose bed, garden soil, garden candidate, garden dormant, garden foreign, garden core"
    assert pick_lines(home, query) == ("like: garden soil", "like: raised beds", "like: rose bed")
    assert pick_lines(home, "paint the chart") == ()
    _insert(memory_store_sql, "수채화")
    assert pick_lines(home, "수채화를 그려 보자") == ("like: 수채화",)
    assert pick_lines(home, "담수채화지") == ()


def test_core_summary_uses_the_promoted_direction_across_daily_score_changes(tmp_path, memory_store_sql):
    from hermes_cli.heart.taste_context import core_lines

    home = tmp_path / "profiles" / "alpha"
    tid = _insert(memory_store_sql, "raised beds", "core")
    memory_store_sql(
        "INSERT INTO memory.taste_events(agent_id,taste_id,reason,changed_by,from_status,to_status,to_valence) "
        "VALUES ('alpha',%s,'status_changed','review','active','core','like')", (tid,),
    )
    _insert(memory_store_sql, "foreign core", "core", agent="beta")
    _insert(memory_store_sql, "active only")
    assert core_lines(home) == ("like: raised beds",)
    memory_store_sql("UPDATE memory.tastes SET valence='dislike',strength=0.2 WHERE id=%s", (tid,))
    assert core_lines(home) == ("like: raised beds",)

    # Imported ledgers can begin at core with no promotion event. The first
    # daily rescore still records the direction that was visible at import.
    imported = _insert(memory_store_sql, "pencil sketches", "core")
    memory_store_sql(
        "INSERT INTO memory.taste_events(agent_id,taste_id,reason,changed_by,from_status,to_status,"
        "from_valence,to_valence) VALUES ('alpha',%s,'valence_changed','review','core','core','like','dislike')",
        (imported,),
    )
    memory_store_sql("UPDATE memory.tastes SET valence='dislike' WHERE id=%s", (imported,))
    assert core_lines(home) == ("like: raised beds", "like: pencil sketches")
