"""ACP launch flags owned by the adapter console entry point."""


def apply_launch_flags(args):
    """Carry explicit console flags to gateway session creation."""
    import os
    # The gateway-backed server forwards launch flags through their documented env equivalents.
    for attr, name in (("accept_hooks", "HERMES_ACCEPT_HOOKS"), ("ignore_rules", "HERMES_IGNORE_RULES"),
                       ("yolo", "HERMES_YOLO_MODE"), ("safe_mode", "HERMES_SAFE_MODE"),
                       ("ignore_user_config", "HERMES_IGNORE_USER_CONFIG")):
        if getattr(args, attr, False):
            os.environ[name] = "1"
    if getattr(args, "safe_mode", False):
        os.environ["HERMES_IGNORE_USER_CONFIG"] = "1"
    # -m / --provider: the route a --safe-mode / --ignore-user-config session freezes (it reads no
    # profile default), carried like the TUI launcher's.
    for attr, name in (("model", "HERMES_MODEL"), ("provider", "HERMES_INFERENCE_PROVIDER")):
        if getattr(args, attr, None):
            os.environ[name] = getattr(args, attr)
