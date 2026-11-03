# Synthetic work record

`ledger.jsonl` and `state.json` are the synthetic record of the work ledger's golden check, with neutral ids
(agent `alpha` shown as 알파, area `Dev`). `work-json.txt`, `work-list.txt` and `work-list-all.txt` are the outputs
of the ledger before its move into LINA, captured on the same synthetic record, with the agent id, its display name
and the area mapped the same way, the agent table reduced to the configured agents and `fields` set to the version
of LINA's writing rules. `test_work_golden.py` compares `lina work json --all`, `list` and `list --all` on this
record imported into the store. Do not regenerate them to accommodate an unintended behaviour change.
