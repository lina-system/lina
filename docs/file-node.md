# File node

A file node is a work machine that holds files (a NAS, for example) and lets omo manage them directly. It takes
work only when named with `lina worker submit --host <name>`, never takes repository work, and a run there can reach
the whole volume (lina#229).

## Shape

```
hub                                               file node (ssh only)
lina worker hub ── FileNodeTransport ── ssh ──>   bash -c '<short script>'
  run state: <worker root>/file_nodes/<device>/     $HOME/<root>/<ID>/work/{input,out,report.md}
  tasks/<ID>/ (job, prompts, slots, manifest,       runner.sh  (nohup setsid; drives omo host session)
  end state, denied-folder check)                   omo + its daemon (installed by the operator)
model path: one ssh session from the hub carries the node's 127.0.0.1:10100 to the hub's OCX
```

- **The hub keeps the run.** `hermes_cli/worker/file_node.py` answers the `_device` verbs (`hello`, `probe`,
  `reserve`, `put`, `untar`, `meta`, `send`, `follow_up`, `human`, `answer`, `cancel`, `pack`) on the hub. The task
  folder lives in the usual device layout under `file_nodes/<device>/` beside the hub's state, so slots,
  manifests, `pack` and the end decision are the same code as on any device.
- **The node runs bash and omo only.** Each verb that needs the node is one short bash script
  (`hermes_cli/worker/file_node_bash.py`) sent as `bash -c` over ssh: the node's login shell is not bash. A run
  starts `runner.sh` with `nohup setsid`, so it outlives the ssh call without a service manager. The runner opens
  or prompts the omo session, follows its lifecycle (`omo host session state|wait`) until it is sealed, cancelled
  or at `worker.hard_timeout_s`, reads the final messages and writes its end marks (`rc.<n>` last). At the hard
  limit it first sends `host session abort` and waits (30 s) to see the session stop; the run ends `stalled`, and
  when the session was not seen to stop the hub aborts it once more before the slot is freed.
- **Its own rules.** The first prompt of a file-node run carries the node's rules instead of the work-folder ones:
  move and change files only inside the paths the brief names, never read or write the device's `deny` entries
  (listed one by one), delete nothing (move it and list every move in report.md; permanent deletion is the
  owner's), and report any other outside effect instead of doing it. The report.md contract stays the same.
- **The hub judges the end.** On `meta` (the hub tick, `--wait`, `status`) the hub asks whether the runner is still
  the recorded process (pid and start time, never a bare pid), and once `rc.<n>` is there copies `report.md`,
  `out/`, the session state and the input hashes back, decides `done`/`done_empty`/`review`/`stalled`/`failed`/
  `cancelled` with the same rules as other devices, and runs the denied-folder check.
- **Nothing on the node needs Python 3.11 or a service manager.** The only process the hub leaves there is
  `runner.sh` (and the time-limited denied-folder look). The node needs bash 4, GNU find, tar, sha256sum, setsid,
  nohup, curl and coreutils; `timeout`, `pgrep`, `getconf` and git are not used.
- **omo is the operator's.** LINA neither installs nor pins omo on a file node; `hello` reports its version.

## Configuration

```yaml
devices:
  workers:
    files:
      host: files                    # ssh alias
      kind: file                     # pick the file-node transport
      role: files                    # the default for kind: file
      omo: "~/.bun/bin/omo"          # omo on the node
      deny: [/volume1/backup, "#recycle", "#snapshot", "@*"]
      volumes: [/volume1]            # where the folder-name patterns are looked for
      root: lina-file-node           # task folders under the node's $HOME (default)
      model_check: http://127.0.0.1:10100/healthz   # default
      scan_s: 300                    # time limit of the denied-folder look (default)
worker:
  access: {files-agent: full}        # a file node runs with access full only
```

`role` applies to every device: `work` (the default; bid on), `files` (only by `--host`, never with `--repo`),
`person` (never takes work, even when named). A run on a file node asks for `--access full`: the session reaches
the whole volume as the node's user, so no narrower mode can be enforced there, and the node says so in its
`hello` (`access_modes: [full]`).

## Model path

`hello` and `probe` ask the node for `model_check` with curl. Anything but HTTP 200 gives `model_path: down`,
`ready: false` and one reason line, which `lina worker hosts`, `lina devices` and the bid table show. A submit with
`--host` onto that node is refused with the same line; a queued run starts only when it answers again.

## Denied folders

`deny` lists absolute folders and folder-name patterns. A brief whose text names a path under one, or an input
whose path (or a file inside an input folder) falls under one, is refused at submit, before the node is asked
anything.

After a run the node looks for files changed under the denied folders since the run started: the absolute folders,
and folders matching a name pattern up to three levels below each entry of `volumes`. The look runs in its own
session and is killed after `scan_s` seconds (backup folders can hold terabytes); a `find` that exits non-zero
(an unreadable folder, an I/O error) has not seen everything either.

Only files owned by the run's user (the node account's uid) count against the run. Backup, sync and indexing
services write under those folders all the time as their own users: measured over 15 minutes on a real node, 552
files changed on the backup share, 75 under the drive sync folder, 16 under the backup service's folder and 4 in
index folders inside a share, and none of them belonged to the node account. They are reported as a count
("changed by other users: n, not counted") and do not end a run as `review`.

One changed file owned by the run's user, or a look that did not finish (time limit, a find error), ends a `done`
run as `review`; the end notice and the card comment name the files (up to 50) or say why the look was not
finished.

This is a check, not a sandbox. The run can reach the whole volume (the node has no user namespaces), and the check
only finds changes after the fact. A run that edits another user's existing file in place leaves that file's owner
unchanged, so the check does not count it.

## Limits

- No answer channel to an omo question: a run waiting on a question shows `waiting for an answer`; send a
  follow-up (it is steered into the turn) or cancel it.
- Stalls are not judged on the node: a run that never seals its lifecycle is aborted at `worker.hard_timeout_s`
  and ends `stalled`.
- A model id containing `/` cannot be passed to `omo host session open --model`; such a run uses omo's own model.

## Why this shape

- **Not LINA on the node.** LINA needs Python 3.11 and git; the node has Python 3.8 and no git, and installing a
  second Python and toolchain onto a storage appliance is upkeep on a box whose updates the vendor owns.
- **Not a service manager.** The node has no user systemd units. `nohup setsid` gives the runner its own session,
  which is all a run needs to outlive the ssh call.
- **The hub owns the state.** It already keeps the job, the slots and the end decision; keeping the task folder
  there too means a lost node session never loses the run's record, and the node stays replaceable.
