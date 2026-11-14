"""Copy only vectors with the destination embedding model and width."""

from array import array

from hermes_cli.migrate_memory_lcm_rows import source_rows, digest


def transfer_vectors(conn, source, tables, receipt, ledger, replay, left_chunks=frozenset()) -> tuple[dict, list]:
    from agent.embedding_client import home_model

    model = home_model()
    profiles = {row["identity_hash"]: row for _, row in source_rows(source, "lcm_embedding_profile")} \
        if "lcm_embedding_profile" in tables else {}
    counts, skipped = {}, []
    for table, identity in (("lcm_embedding_vectors", "embedded_id"), ("lcm_chunk_vectors", "chunk_id")):
        if table not in tables:
            continue
        copied = deferred = left = total = 0
        for native, row in source_rows(source, table):
            total += 1
            if table == "lcm_chunk_vectors" and row["chunk_id"] in left_chunks:
                left += 1
                continue
            profile = profiles.get(row["identity_hash"], {})
            value = row["vec"]
            compatible = (profile.get("model_name") == model and profile.get("dim") == 1024
                          and profile.get("dtype", "float32") == "float32"
                          and profile.get("byteorder", "little") == "little"
                          and isinstance(value, bytes) and len(value) == 4096)
            if not compatible:
                deferred += 1
                continue
            mapping = receipt["ids"].setdefault(table, {})
            if not replay:
                conn.execute(f"INSERT INTO {table}({identity},identity_hash,vec) VALUES (?,?,?)",
                             (row[identity], row["identity_hash"], value))
            actual = conn.execute(f"SELECT rowid,vec FROM {table} WHERE {identity}=? AND identity_hash=?",
                                  (row[identity], row["identity_hash"])).fetchone()
            if actual is None or array("f", actual[1]) != array("f", value):
                raise ValueError(f"changed imported LCM vector: {table}/{native}")
            if replay and mapping.get(str(native)) != actual[0]:
                raise ValueError(f"changed imported LCM vector identity: {table}/{native}")
            mapping[str(native)] = actual[0]
            sha = digest({identity: row[identity], "identity_hash": row["identity_hash"], "vec": value})
            for side in ("source", "destination"):
                ledger.execute(f"INSERT INTO {side}_manifest VALUES (?,?)", (f"{table}:{native}", sha))
            copied += 1
        counts[table] = {"source": total, "matched": copied if replay else 0,
                         "inserted": 0 if replay else copied, "skipped": deferred + left}
        if deferred:
            skipped.append({"kind": table, "rows": deferred, "reason": "incompatible_model_width_or_dtype_reembed"})
        if left:
            skipped.append({"kind": table, "rows": left, "reason": "fork_quarantine_uncited"})
    return counts, skipped
