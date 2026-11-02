"""Fakes for LINA's pinned OCX (lina#32): an installed version whose ``bun`` is a small Python program.

The fake ``bun`` is called exactly as the real one (``bun <OCX entry> <verb…>``). ``start --port N`` serves
``/healthz`` and ``/v1/models`` on 127.0.0.1:N and records its pid in ``$OPENCODEX_HOME/ocx.pid``; ``/v1/models``
answers with the status written in ``$OPENCODEX_HOME/fake-status`` (200 when absent). Any other verb prints the
recorded ``--json`` output of OCX 2.95.0 from ``fixtures/ocx_2_95_0``; ``integration client enable --client omo`` writes
a one-model catalog to ``$OMO_CODING_AGENT_DIR/models.json`` (refused, like OCX, when that dir does not exist).

Like OCX 2.95.0's start (``discoverLiveProxy`` + ``start-owner-fence.ts``), ``start`` first probes the port in
``$OPENCODEX_HOME/config.json`` (else the default port, never ``--port``) and refuses with "Proxy ownership is
unverified; startup refused" when a proxy this data dir did not record answers there. The default port is the one
passed to ``install_fake_version`` (the real 10100 is never probed by a test); without one there is no fallback.
"""
import contextlib
import json
import os
import sys
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

from hermes_cli import lina_ocx

FIXTURES = Path(__file__).parent / "fixtures" / "ocx_2_95_0"
VIEW_FIXTURES = {"models live --json": "models_live.json", "account list --json": "account_list.json",
                 "status --json": "status.json", "usage --json": "usage.json"}

_FAKE_BUN = '''#!{python}
import http.server, json, os, sys, urllib.request
args = sys.argv[2:]
home = os.environ["OPENCODEX_HOME"]
if args[:1] == ["start"]:
    port = int(args[args.index("--port") + 1])
    config_file = os.path.join(home, "config.json")
    config = json.load(open(config_file)) if os.path.exists(config_file) else {{}}
    probe = config.get("port") or {default_port!r}
    if probe:
        try:
            with urllib.request.urlopen("http://127.0.0.1:%d/healthz" % probe, timeout=2) as response:
                live = json.load(response)
        except OSError:
            live = None
        pid_file = os.path.join(home, "ocx.pid")
        recorded = open(pid_file).read().strip() if os.path.exists(pid_file) else None
        if live is not None and str(live.get("pid")) != recorded:
            print("Proxy ownership is unverified; startup refused.", file=sys.stderr)
            sys.exit(1)
    config["port"] = port
    with open(config_file, "w") as fh:
        json.dump(config, fh)
    status_file = os.path.join(home, "fake-status")
    with open(os.path.join(home, "argv"), "w") as fh:
        json.dump(sys.argv, fh)

    class Handler(http.server.BaseHTTPRequestHandler):
        def do_GET(self):
            if self.path == "/healthz":
                code, body = 200, {{"status": "ok", "service": "opencodex", "pid": os.getpid(), "port": port}}
            else:
                code = int(open(status_file).read()) if os.path.exists(status_file) else 200
                body = {{"object": "list", "data": []}}
            payload = json.dumps(body).encode()
            self.send_response(code)
            self.send_header("Content-Length", str(len(payload)))
            self.end_headers()
            self.wfile.write(payload)

        def log_message(self, *a):
            pass

    server = http.server.HTTPServer(("127.0.0.1", port), Handler)
    with open(os.path.join(home, "ocx.pid"), "w") as fh:
        fh.write(str(os.getpid()))
    server.serve_forever()
if args == ["integration", "client", "enable", "--client", "omo"]:  # OCX writes its catalog where omo reads it
    agent = os.environ.get("OMO_CODING_AGENT_DIR") or os.path.join(os.environ["HOME"], ".omo", "agent")
    if not os.path.isdir(agent):
        print("omo is not installed", file=sys.stderr)
        sys.exit(1)
    with open(os.path.join(agent, "models.json"), "w") as fh:
        json.dump({{"providers": {{"opencodex": {{"models": [{{"id": "gpt-fake"}}]}}}}}}, fh)
    print("omo enabled.")
    sys.exit(0)
fixtures = {fixtures!r}
name = fixtures.get(" ".join(args))
if name is None:
    print("unknown command: " + " ".join(args), file=sys.stderr)
    sys.exit(2)
sys.stdout.write(open(os.path.join({fixture_dir!r}, name)).read())
'''


@contextlib.contextmanager
def other_ocx():
    """Another OCX on the account (the person's own, or LINA's running service seen from a data copy): it answers
    ``/healthz`` with an identity no fresh data dir records. Yields its port."""

    class Health(BaseHTTPRequestHandler):
        def do_GET(self):
            body = json.dumps({"status": "ok", "service": "opencodex", "pid": 4_000_000,
                               "instanceId": "home_other"}).encode()
            self.send_response(200)
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def log_message(self, *args):
            pass

    server = ThreadingHTTPServer(("127.0.0.1", 0), Health)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        yield server.server_address[1]
    finally:
        server.shutdown()
        server.server_close()
        thread.join()


def install_fake_version(root: Path, version: str, default_port: int | None = None) -> Path:
    """``vendor/ocx/<version>`` with the fake ``bun`` at the path the real package puts it."""
    bun, entry = lina_ocx.entry_paths(root, version)
    bun.parent.mkdir(parents=True, exist_ok=True)
    entry.parent.mkdir(parents=True, exist_ok=True)
    entry.write_text("// OCX CLI entry (fake)\n")
    bun.write_text(_FAKE_BUN.format(python=sys.executable, fixtures=VIEW_FIXTURES, fixture_dir=str(FIXTURES),
                                    default_port=default_port))
    bun.chmod(0o755)
    return lina_ocx.version_dir(root, version)


def installed_root(root: Path, version: str = "2.95.0", default_port: int | None = None) -> Path:
    """A LINA root with *version* installed and current, and an empty OCX data dir."""
    install_fake_version(root, version, default_port)
    lina_ocx.set_current(root, version)
    lina_ocx.data_dir(root).mkdir(parents=True, exist_ok=True)
    os.makedirs(lina_ocx.home_dir(root), exist_ok=True)
    return root
