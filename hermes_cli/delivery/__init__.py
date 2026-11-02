"""``lina work next|merge|status|sprint`` (lina-system/lina#63): the delivery procedure of docs/delivery.md from the
core.

* ``next.py`` picks the issues to open (closed dependencies, current sprint, issues waiting behind it, contract
  scopes) from one snapshot (``collect``) through a pure ``decide``, and ``--submit`` hands each pick to the runner
  its label names through ``lina worker`` (#35).
* ``merge.py`` is the only merge path: ``hermes_cli.merge_rules.may_merge`` decides, then a merge commit or a merge
  queue entry, then the linked issue is closed with its Done when ticked and P2/P3 findings go to follow-up issues
  (``followups.py``). ``land.py`` lands a worker branch by merging ``dev`` into it, never by re-applying patches.
* ``status.py`` shows sprint progress per milestone, open pull requests with their gate state and running jobs.
* ``record.py`` is the seam to the work record: running jobs come from ``lina worker``'s hub today and from
  ``lina work``'s ledger (#36) once it lands.
* ``settings.py`` reads ``work:`` in the LINA root's ``config.yaml``; the current sprint is the owner's setting.
"""
