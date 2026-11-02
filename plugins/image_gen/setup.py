"""Image-generation startup contributions, owned by the generation tool."""

from tools.registry_contributions import StartupContribution


def fal_catalog(config):
    from tools.image_generation_catalog import FAL_MODELS, DEFAULT_MODEL
    return FAL_MODELS, DEFAULT_MODEL


def managed_catalog(config):
    from hermes_cli.tools_config import get_nous_subscription_features
    from tools.image_generation_managed import managed_image_catalog

    acct = get_nous_subscription_features(config).account_info
    pool_only = bool(acct and acct.logged_in and acct.paid_service_access is not True)
    return managed_image_catalog(
        include_krea=not pool_only or acct.tool_gateway_entitled_for("krea"), include_portal=not pool_only)


def managed_partner(config):
    from tools.image_generation_managed import FAL, KREA, PORTAL, managed_route

    section = config.get("image_gen")
    section = section if isinstance(section, dict) else {}
    return {FAL: "FAL", KREA: "Krea", PORTAL: "Nous Portal"}.get(
        managed_route(section.get("provider"), section.get("model")))


def contribution():
    from plugins.image_gen.rpc import register

    return StartupContribution(
        bundles=("core", "safe"), description="Creative generation tools (images)",
        checklist=("🎨 Image Generation", "image_generate"),
        category={"name": "Image Generation", "icon": "🎨", "providers": [{
            "name": "Nous Subscription", "badge": "subscription",
            "tag": "Managed image generation (FAL, Krea 3 and Krea 2, Nous Portal models) billed to your subscription",
            "env_vars": [], "requires_nous_auth": True, "managed_nous_feature": "image_gen",
            "override_env_vars": ["FAL_KEY"], "imagegen_backend": "nous"}]},
        catalogs={
            "fal": {"display": "FAL.ai", "config_key": "image_gen", "catalog_fn": fal_catalog},
            "nous": {"display": "Nous Subscription", "config_key": "image_gen", "catalog_fn": managed_catalog}},
        managed_partner=managed_partner, rpc_installer=register,
        distributions={
            **{name: {"toolsets": {"image_gen": probability}} for name, probability in (
                ("default", 100), ("science", 15), ("safe", 60), ("balanced", 50),
                ("terminal_tasks", 10), ("mixed_tasks", 15))},
            "image_gen": {"description": "Heavy focus on image generation with vision and web support",
                          "toolsets": {"image_gen": 90, "vision": 90, "web": 55, "terminal": 45}},
            "creative": {"description": "Image generation and vision analysis focus",
                         "toolsets": {"image_gen": 90, "vision": 90, "web": 30}},
        },
    )
