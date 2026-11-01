"""Installed generation tools contribute the RPC without a kept-code import."""

import json
from types import SimpleNamespace

from utils import is_truthy_value


def test_discovery_installs_image_rpc_and_dispatches_with_existing_result_shape(monkeypatch):
    from tools import image_generation_tool
    from tui_gateway import methods_images

    handlers = {}
    server = SimpleNamespace(
        register_method=lambda name, fn: handlers.__setitem__(name, fn),
        _ok=lambda rid, result: {"id": rid, "result": result},
        _err=lambda rid, code, message: {"id": rid, "error": {"code": code, "message": message}},
        json=json, is_truthy_value=is_truthy_value,
    )
    monkeypatch.setattr(image_generation_tool, "check_image_generation_requirements", lambda: True)
    calls = []

    def generate(args):
        calls.append(args)
        return json.dumps({"success": True, "image": "data:image/png;base64,AAAA"})

    monkeypatch.setattr(image_generation_tool, "_handle_image_generate", generate)
    methods_images.register(server)

    assert handlers["image.generate"](1, {"probe": True}) == {"id": 1, "result": {"available": True}}
    result = handlers["image.generate"](2, {"prompt": "a tree", "aspect_ratio": "portrait"})
    assert calls == [{"prompt": "a tree", "aspect_ratio": "portrait"}]
    assert result == {"id": 2, "result": {"available": True, "success": True, "image": "data:image/png;base64,AAAA"}}
