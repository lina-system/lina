"""Fakes for LINA's pinned OCX (lina#32): an installed version whose ``bun`` is a small Python program.

The fake ``bun`` is called exactly as the real one (``bun <OCX entry> <verb…>``). ``start --port N`` serves
``/healthz`` and ``/v1/models`` on 127.0.0.1:N and records its pid in ``$OPENCODEX_HOME/ocx.pid``; ``/v1/models``
answers with the status written in ``$OPENCODEX_HOME/fake-status`` (200 when absent). Any other verb prints the
recorded ``--json`` output of OCX 2.95.0 from ``fixtures/ocx_2_95_0``.
"""
import os
import sys
from pathlib import Path

from hermes_cli import lina_ocx

FIXTURES = Path(__file__).parent / "fixtures" / "ocx_2_95_0"
VIEW_FIXTURES = {"models live --json": "models_live.json", "account list --json": "account_list.json",
                 "status --json": "status.json", "usage --json": "usage.json"}

_FAKE_BUN = '''#!{python}
import http.server, json, os, sys
args = sys.argv[2:]
home = os.environ["OPENCODEX_HOME"]
if args[:1] == ["start"]:
    port = int(args[args.index("--port") + 1])
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
fixtures = {fixtures!r}
name = fixtures.get(" ".join(args))
if name is None:
    print("unknown command: " + " ".join(args), file=sys.stderr)
    sys.exit(2)
sys.stdout.write(open(os.path.join({fixture_dir!r}, name)).read())
'''


def install_fake_version(root: Path, version: str) -> Path:
    """``vendor/ocx/<version>`` with the fake ``bun`` at the path the real package puts it."""
    bun, entry = lina_ocx.entry_paths(root, version)
    bun.parent.mkdir(parents=True, exist_ok=True)
    entry.parent.mkdir(parents=True, exist_ok=True)
    entry.write_text("// OCX CLI entry (fake)\n")
    bun.write_text(_FAKE_BUN.format(python=sys.executable, fixtures=VIEW_FIXTURES, fixture_dir=str(FIXTURES)))
    bun.chmod(0o755)
    return lina_ocx.version_dir(root, version)


def installed_root(root: Path, version: str = "2.95.0") -> Path:
    """A LINA root with *version* installed and current, and an empty OCX data dir."""
    install_fake_version(root, version)
    lina_ocx.set_current(root, version)
    lina_ocx.data_dir(root).mkdir(parents=True, exist_ok=True)
    os.makedirs(lina_ocx.home_dir(root), exist_ok=True)
    return root
