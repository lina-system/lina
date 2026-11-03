"""``lina worker`` (lina#35): run, settle and answer coding-agent work on a work device, and hand it out from the hub.

Two sides share one package:

* Device side (``device.py``, ``serve.py``): ``lina worker serve`` runs as the service ``ai.lina.worker`` /
  ``lina-worker``. It takes the runs the hub drops into ``$LINA_HOME/worker/tasks/<ID>/``, prepares the task folder
  and its worktree (repo cache ``$LINA_HOME/worker/repos/``), starts the runner, waits until the run settled, writes
  ``manifest.json`` and reports the end back to the hub.
* Hub side (``hub.py``): ``lina worker submit|send|status|result|accept|reject|cancel|list|receipts|hosts`` with
  bidding over the devices in ``devices.workers`` (``bidding.py``), schedules, groups, receipts, verdicts, the report
  contract (``report.py``) and one end notice to the owning agent's chat and, for a run started from a work card,
  on that card (``notice.py``, ``kanban_link.py``). Hub state lives in ``$LINA_HOME/worker/hub/``.

The hub reaches a device through ``transport.py`` (ssh today; one small interface so a Node channel can replace it)
and the device reports back the same way. Coding agents plug in through ``runners/``: omo is the default runner,
and a ``command`` runner registered in config runs any CLI that reads a prompt on stdin.

The state root is LINA's own: today's ``omo-task`` state, its device folders and its runner are never read, written
or started from here, so both can run side by side on one device.
"""
