# internal/core/jobs implementation notes

## Implementation notes

Design authority: [Go Core](../../../docs/design/go-core.md) and the contracts it cites.
These notes retain implementation and validation history from the cited increments;
they do not establish current acceptance or change the contracts.

### C-56 job API increment

The job API increment exposes `Aux`, `Apply` and `Report` over bounded HTTP/JSON requests on that Unix socket.
Listening requires the held Core state-root lock, kept for the listener lifetime.
Under that lock a restart may reclaim an existing 0600 socket only when a connection probe returns `ECONNREFUSED`; a live listener, uncertain probe, non-socket path, or non-private socket/directory is refused without changing that path.
The types and client live in `internal/moirai/jobapi`; `internal/core/jobs` owns the service.
Core registers each job's model callback, account binding and transactional write-rule callback.
Neither SQL, credentials, provider selection nor tool authority cross this API.
Auxiliary calls receive no persona instructions.
A deterministic growth job may carry its precomputed growth snapshot candidate as data; Core binds the target agent and refuses other persona layers (C-23).
A Core schema owner includes `jobs.Schema` and the shared budget schema in its numbered migrations; neither serving the API nor starting a worker creates or migrates canon.
Concrete memory/planning algorithms and installation remain their own roadmap increments.
