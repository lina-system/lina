"""Host-wide settings and absolute limits for manual collectors."""

CALL_LIMITS = {"mail": (10, 20, 100), "slack": (15, 30, 150)}
LIMIT_KEYS = ("max_calls_per_run", "max_calls_per_hour", "max_calls_per_day")
MAIL_QUERY = "newer_than:2d in:inbox -in:spam -in:trash -category:promotions"


def source_defaults(kind: str) -> dict[str, object]:
    settings: dict[str, object] = dict(zip(LIMIT_KEYS, CALL_LIMITS[kind]))
    settings.update(enabled=False, min_interval_minutes=60)
    if kind == "mail":
        settings.update(accounts=[], query=MAIL_QUERY)
    return settings
