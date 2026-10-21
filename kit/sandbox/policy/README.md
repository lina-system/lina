# Sandbox policy provenance

Policy identifier: `lina-sandbox-1`.

The policy follows the Apache-2.0 [OpenAI Codex sandbox source](https://github.com/openai/codex/tree/c7234c1174b27d9c55199fba0d546dc27d1b0415/codex-rs), pinned at `c7234c1174b27d9c55199fba0d546dc27d1b0415`:

- `linux-sandbox/src/seccomp.rs`: deny process inspection, io_uring and VM sockets; block network syscalls when network is not authorized.
- `sandboxing/src/seatbelt_base_policy.sbpl`: default deny, inherited process confinement, signals restricted to the sandbox, and writes to `/dev/null`.

LINA uses a small Go policy compiler instead of Codex's Rust seccompiler. It additionally denies namespace/mount changes, kernel keyring access, process-group escape and alternate x86 syscall ABIs. Bubblewrap supplies the user, PID and default network namespaces and no-new-privileges. Writable roots are explicit; protected paths override grants. Seatbelt denies protected file access even when network is authorized, and permits no keychain Mach service.

`go generate ./kit/sandbox` regenerates the four checked-in classic BPF artifacts (Linux amd64 and arm64, each with blocked or authorized network). They are embedded in the binary at build time and passed to bubblewrap through an unlinked temporary file descriptor. No runtime policy compiler or linked bubblewrap library is needed. Other Linux architectures and Windows fail closed.

The wrapper exposes the policy identifier for release-manifest integration. Packaging's manifest schema does not yet have a sandbox-policy field; adding that field belongs to packaging, outside this issue's files.
