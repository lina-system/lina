"""Shared profile catalog redirect-credential contract."""

import json

from providers import get_provider_profile

def test_actual_profile_fetch_models_sends_credential_only_to_original_origin(
    monkeypatch,
):
    """fetch_models must route through the shared redirect-credential guard.

    ActualProfile overrides ProviderProfile.fetch_models with its own
    base_url resolution, and previously called raw urllib.request.urlopen
    directly instead of the base class's open_credentialed_url — losing the
    protection that strips the Authorization header when a redirect leaves
    the original host. Exercises the real SafeCredentialRedirectHandler
    (no mocking of open_credentialed_url itself) against a local HTTP
    server that 302s to a different origin, mirroring
    test_urllib_security.py's end-to-end redirect tests.
    """
    import http.server
    import threading

    profile = get_provider_profile("ocx")

    source_auth_headers: list[str | None] = []
    target_auth_headers: list[str | None] = []

    class _RedirectTargetHandler(http.server.BaseHTTPRequestHandler):
        def do_GET(self):
            target_auth_headers.append(self.headers.get("Authorization"))
            body = json.dumps({"data": [{"id": "should-not-be-trusted"}]}).encode()
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def log_message(self, *_args):
            pass

    target_server = http.server.HTTPServer(("127.0.0.1", 0), _RedirectTargetHandler)
    target_thread = threading.Thread(target=target_server.serve_forever, daemon=True)
    target_thread.start()
    target_port = target_server.server_address[1]

    class _RedirectingHandler(http.server.BaseHTTPRequestHandler):
        def do_GET(self):
            source_auth_headers.append(self.headers.get("Authorization"))
            self.send_response(302)
            self.send_header("Location", f"http://127.0.0.1:{target_port}/models")
            self.end_headers()

        def log_message(self, *_args):
            pass

    redirect_server = http.server.HTTPServer(("127.0.0.1", 0), _RedirectingHandler)
    redirect_thread = threading.Thread(
        target=redirect_server.serve_forever, daemon=True
    )
    redirect_thread.start()
    redirect_port = redirect_server.server_address[1]

    try:
        result = profile.fetch_models(
            api_key="actual-secret-token",
            base_url=f"http://127.0.0.1:{redirect_port}",
            timeout=5.0,
        )
    finally:
        redirect_server.shutdown()
        target_server.shutdown()
        redirect_thread.join(timeout=2.0)
        target_thread.join(timeout=2.0)

    assert result == ["should-not-be-trusted"], (
        "sanity check: the redirect must actually have been followed"
    )
    assert source_auth_headers == ["Bearer actual-secret-token"]
    assert target_auth_headers == [None], (
        "Authorization header leaked to a different origin after a redirect"
    )
