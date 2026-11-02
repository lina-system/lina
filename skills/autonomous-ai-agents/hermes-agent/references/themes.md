# CLI Skins

Skins are YAML data read by `hermes_cli/skin_engine.py`. Use `read_file` to inspect the active palette before changing it.

1. For a new palette, copy `templates/skin.yaml` from this skill to `$HERMES_HOME/skins/<name>.yaml` with `write_file`. Choose a unique name and readable text/background contrast.
2. Activate it yourself with `lina config set display.skin <name>` or `lina skin use <name>`. Do not hand-edit config YAML.
3. Read back `lina config get display.skin` and inspect the CLI result. Do not assume a live repaint occurred merely because the write succeeded.
4. For one color, use `lina skin set <key> <hex>`. This edits the active skin while preserving the rest of its palette; do not start from default for a small tweak.

Use `lina skin list` to enumerate installed skins. Quote hex values in shell commands. Keep six-digit hex colors; missing keys inherit defaults. `ui_tool` controls tool markers, `ui_thinking` reasoning text, `ui_accent` shared accents, and `background` the base. Verify accepted keys against the installed engine.

To revert, select the previous skin with `lina skin use <name>`. Verify normal, warning, error, tool, and prompt text remain legible.
