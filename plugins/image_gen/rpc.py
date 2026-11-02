"""Image generation RPC installed alongside the optional tool."""

from tui_gateway.method_ctx import HandlerRegistry, bind_module

_registry = HandlerRegistry()
method = _registry.method


@method("image.generate")
def _(rid, params: dict) -> dict:
    """Generate through the same dispatcher as the model tool."""
    try:
        from tools.image_generation_tool import check_image_generation_requirements
        available = bool(check_image_generation_requirements())
    except Exception:
        available = False
    if is_truthy_value(params.get("probe", False)):
        return _ok(rid, {"available": available})
    if not available:
        return _ok(rid, {
            "available": False, "success": False,
            "error": "No image generation backend configured (run `hermes tools` to enable one)."})
    prompt = str(params.get("prompt") or "").strip()
    if not prompt:
        return _err(rid, 4071, "prompt required")
    aspect = str(params.get("aspect_ratio") or "square").strip().lower()
    try:
        cap = min(int(params.get("max_bytes", 8_000_000) or 8_000_000), 16_000_000)
    except (TypeError, ValueError):
        cap = 8_000_000
    try:
        from tools.image_generation_tool import _handle_image_generate
        result = json.loads(_handle_image_generate({"prompt": prompt, "aspect_ratio": aspect}))
    except Exception as e:
        return _err(rid, 5071, str(e))
    if not result.get("success"):
        return _ok(rid, {"available": True, "success": False,
                         "error": str(result.get("error") or "generation failed")})
    image_ref = str(result.get("image") or "")
    data_url = _image_to_data_url(image_ref, cap) if image_ref else None
    return _ok(rid, {"available": True, "success": True, "image": image_ref,
                     **({"image_data": data_url} if data_url else {})})


def register(server) -> None:
    bind_module(globals(), server, skip=("_",))
