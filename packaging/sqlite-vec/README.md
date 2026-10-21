# Packaged SQLite vector engine

The platform-independent engine in `internal/core/canon/sqlite-vec.wasm` links
sqlite-vec v0.1.6 into the SQLite 3.51.2 Wasm engine of
`github.com/ncruces/go-sqlite3` v0.30.5. It retains that binding's ABI and its
FTS5, WAL and online-backup behavior. Canon verifies the complete engine's
SHA-256 before initialization:

```text
2eaa33d000922ae5369d6fbf55be07ae03f68ebb7bf82ec49e2657282e37718a
```

This is the same engine proven by the [binding acceptance](../../evidence/sqlite-binding.json).
It is embedded in `lina`, so no shared library, extra runtime, file lookup or
network fetch is needed at service start. Having the extension available does
not configure an embedding provider or change FTS5 recall policy.

To reproduce it on Linux x86-64, with Go, Bash, Python 3, curl, tar, patch,
OpenSSL, realpath and sha256sum available:

```sh
bash packaging/sqlite-vec/build.sh /tmp/sqlite-vec-rebuilt.wasm
cmp /tmp/sqlite-vec-rebuilt.wasm internal/core/canon/sqlite-vec.wasm
```

The output must not exist. The script uses a disposable directory, verifies
the Go module sums, SQLite source archive, wasi-sdk 29 and Binaryen 125
archives, and refuses to write the result unless the complete engine matches
the pin. Its tools are downloaded only for rebuilding this asset; ordinary Go
and release builds use the checked-in bytes. Moving any pin requires updating
the embedded digest and rerunning the Canon vector, FTS5, backup and tampering
tests. [NOTICE.md](NOTICE.md) carries the redistributed license texts and is
included in both platform archives.

```sh
go test -count=1 ./internal/core/canon/... ./internal/core/manifest/...
```

Cross-builds prove packaging and linkage. They do not establish native macOS
execution or the separate V2 24-hour residency and performance measurements.
