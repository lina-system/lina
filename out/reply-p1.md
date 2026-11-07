Fixed in `{{COMMIT_SHA}}` (`test(lcm): count the paced loop's wake-ups instead of timing it`), which
replaces the wall-clock proof in `test_duty_wait_is_retirement_interruptible` with a wake-up count:
the scheduler's `_condition.wait` is wrapped on the instance so the test records, at entry, every
timeout the worker actually asks for and whether the stop flag was already set, and the paced
continuation's delay stays patched 10,000× so the delay can never end the wait. The `stop` case now
requires every paced wait to be a poll (`<= 0.5 s`, the slice the stop-flag check depends on) and at
most the one wake that sees the stop flag to be entered after the interrupt — no stopwatch, and the
`retire` case keeps its own path through the scheduler's condition. The round-2 version of the test
passes on the same head when `_run`'s poll bound is raised from `0.5` to `5.0` (`2 passed in 6.02s`);
the new one fails there with `AssertionError: [{'timeout': 5.0, 'stop_set': False}]` for both
parameters, fails at the 300 s cap for `stop` when the paced loop ignores the stop flag, and passes on
the real engine (2 passed in 1.5 s); the file is 30/30 on both the SQLite and the Postgres store, and
`check_lcm_x_subtree.py` still reports the subtree consistent.
