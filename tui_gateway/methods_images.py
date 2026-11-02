"""Image-generation JSON-RPC handler (ws twin of the image_generate tool) for UI surfaces
(avatar pickers, artifact panes). The result is a data URL: a remote desktop can't read a
gateway file path and hosted URLs are often CORS-opaque to a renderer canvas. Bodies are
rebound onto server.py's globals (method_ctx.bind_module) and reference them bare.
"""

from .method_ctx import bind_module


def _image_to_data_url(ref: str, cap: int):
    """Fetch a URL or read a local path into a data URL; None when missing, over *cap*, or failing."""
    import base64
    import mimetypes
    import os
    try:
        if ref.startswith(("http://", "https://")):
            # Provider-result URLs are remote-party-controlled — same SSRF guard as
            # agent/provider_media.save_url (which may have fallen back to the bare URL).
            from tools.url_safety import create_ssrf_safe_client, is_safe_url
            if not is_safe_url(ref):
                return None
            with create_ssrf_safe_client(timeout=60, follow_redirects=True) as client, \
                    client.stream("GET", ref, headers={"User-Agent": "hermes-agent"}) as resp:
                resp.raise_for_status()
                if resp.headers.get("content-length") and int(resp.headers["content-length"]) > cap:
                    return None
                chunks, total = [], 0
                for chunk in resp.iter_bytes():
                    total += len(chunk)
                    if total > cap:
                        return None
                    chunks.append(chunk)
                data = b"".join(chunks)
                mime = (resp.headers.get("content-type") or "image/png").split(";", 1)[0].strip()
        elif os.path.isfile(ref):
            if os.path.getsize(ref) > cap:
                return None
            with open(ref, "rb") as fh:
                data = fh.read(cap + 1)
            mime = mimetypes.guess_type(ref)[0] or "image/png"
        else:
            return None
        if len(data) > cap:
            return None
        mime = mime if mime.startswith("image/") else "image/png"
        return f"data:{mime};base64,{base64.b64encode(data).decode('ascii')}"
    except Exception:
        return None


def register(server) -> None:
    from tools.registry_contributions import startup_entries

    bind_module(globals(), server, skip=("_",))
    for entry in startup_entries():
        if entry.startup.rpc_installer:
            entry.startup.rpc_installer(server)
