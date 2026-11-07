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
- **A start that did not reach the node is started again.** There is no serve on the node to pick up a request,
  so `meta` (the hub tick) starts a requested run that has no start mark, at most 5 times a minute apart, then ends
  it `failed` ("launch failed after n tries") and frees its slot. A run whose earlier start did happen (its answer
  was lost) is adopted from its pid file on the node, never started twice. A follow-up takes a slot like a first
  submit; while the device is full it waits (holding no slot) and the one that waited longest takes the next free
  slot, under the slot lock, so waiting follow-ups never block each other or overfill the device.
- **A cancel stops the session on the node too.** A cancel that comes while the session is still opening finds no
  session to abort; the runner aborts it as soon as it is there, and waits to see it stop, before its end marks
  (as at the hard limit; a session not seen to stop is aborted again before the slot is freed). A failed abort is
  reported by `lina worker cancel`, not hidden.
- **A cancel and a start take turns.** A cancel that arrives while a start is still on its way (the upload, or the
  ssh answer) waits for it under the task lock, then cancels the run on the node like any running run; the slot is
  freed only once that run ended. A run that has not started is never started after a cancel.
- **Follow-ups change a run under the task lock.** The next run's number, its prompt (or an instruction added to
  a run still waiting to start) and its request are written under one lock, so two follow-ups at once both keep
  their instruction.
- **The model path is asked again before every start.** A run that starts later than its submit (a waiting
  follow-up, a retried start) does not start while the model path does not answer: it waits with that reason and
  starts on a later tick, and the wait is not counted as a failed start.
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

Add this node on the hub through the work settings, without editing `config.yaml` by hand:

```sh
lina worker settings set devices.workers.files '{host: files, kind: file, role: files, omo: "~/.bun/bin/omo", deny: [/volume1/backup, "#recycle", "#snapshot", "@*"], volumes: [/volume1], root: lina-file-node, model_check: "http://127.0.0.1:10100/healthz", scan_s: 300}'
lina worker settings set worker.access.files-agent full
lina worker settings validate
lina worker hosts
```

Each `set` creates a revision; `lina worker settings rollback` undoes the last change. `kind` defaults to `lina`;
the six file-node fields above are accepted only with `kind: file`. `deny` and `volumes` may be empty lists but
their entries must be non-blank text; `omo` and `root` must be non-blank text, `model_check` an http/https URL
with a host, and `scan_s` a positive integer. Omitted fields use the defaults shown above (`omo: omo` and empty
`deny`/`volumes`). A file node cannot have `role: work` or `role: person`.

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

After a run the node looks for files changed under the denied folders since the run started (a newer modification
time or a newer status-change time, so a file moved or renamed into one is caught too): the absolute folders,
and folders matching a name pattern up to three levels below each entry of `volumes`. The look runs in its own
session and is killed after `scan_s` seconds (backup folders can hold terabytes); a `find` that exits non-zero
(an unreadable folder, an I/O error) has not seen everything either.

Only files owned by the run's user (the node account's uid) count against the run. Backup, sync and indexing
services write under those folders all the time as their own users: measured over 15 minutes on a real node, 552
files changed on the backup share, 75 under the drive sync folder, 16 under the backup service's folder and 4 in
index folders inside a share, and none of them belonged to the node account. They are reported as a count
("changed by other users: n, not counted") and do not end a run as `review`.

A folder moved into a denied folder gets a new status-change time while its old files keep theirs: a folder of the
run's user whose ctime alone changed after the start is listed with everything under it.

The same files can show up twice: on a real node a container app bind-mounted all 15 shares under its own `@appdata`
folder, so the run user's home appeared again under `@*`, and all 59 changed files of one run (omo's own state and the
run folder) were seen through that alias, with 0 under a real denied folder. The node's `/proc/mounts` is read
before the look: a mount point under a denied folder whose source (its btrfs `subvol=`, placed through a mount of the
same device) lies outside every denied folder is skipped ("aliases skipped: n"); one whose source is denied is
looked through. Folders the run's user cannot write are not looked into (`find … -type d ! -writable -prune`): the run could not
have changed anything there, and on a real node the look otherwise ran past its 300 s limit (106 folders it could
not read, 245 changes by other users, 14 aliases). They are counted ("not writable by the run's user, not looked
into: n"), also when the look runs out of time. The gap: a folder the run may write inside one it may not (DSM
ACLs) is not seen either. A folder it may write but not read, or any other find error, still means the look did
not see everything.

Before the session starts, `runner.sh` lists every file in the folders the run's user may write, with the same
rules (alias skips, unwritable folders pruned, snapshots never walked, `nice -n 19`). After the run the same walk is
compared with that list as streams (`sort`, `comm`, `join`): a file of the run's user that is gone (deleted or moved
out) is named and ends a `done` run as `review`; files of other users that are gone are only counted ("Removed,
owned by other users: n"), because backup and Drive services remove and replace files there all the time, and this
is a check, not a sandbox. A list before the run that did not finish leaves the check incomplete. Both walks and
the comparison run under `ulimit -v` 1 GiB (a node with 12 GB of memory once had its processes killed by the
kernel); going over the limit leaves the check incomplete too.

Snapshot folders (`#snapshot`, `@sharesnap`) are never looked into, writable or not: a snapshot is a read-only copy
the run cannot change, and walking the shares' snapshots once loaded a real node to 260, cut its ssh session and
dropped the hub's model-path session three times. The whole look runs at `nice -n 19` (the node has no `ionice`).
Not going into unwritable folders is therefore a safety measure for the node as much as a speed-up.

One changed file owned by the run's user, or a look that did not finish (time limit, a find error), ends a `done`
run as `review`; the end notice and the card comment name the files (up to 50) or say why the look was not
finished.

This is a check, not a sandbox. The run can reach the whole volume (the node has no user namespaces), and the check
only finds changes after the fact. A run that edits another user's existing file in place leaves that file's owner
unchanged, so the check does not count it.

## Limits

- `lina worker status` names the denied-folder check as the reason for a `review` it caused, in the same
  words as the end notice and the card comment.

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
