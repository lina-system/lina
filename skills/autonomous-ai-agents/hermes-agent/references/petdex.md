# Terminal Mascots

The optional pet interface is wired by `hermes_cli/subcommands/pets.py`. Use `lina pets --help` to discover actions supported by the installed pet module before invoking them.

Inspect `display.pet` and the active profile's `pets/` directory. A pet must be installed, selected, and enabled; a pipe without a TTY cannot show terminal graphics. Rendering depends on the terminal's graphics support, with a text fallback where supported. Do not diagnose missing graphics as failed inference.

Keep downloads and selections profile-scoped. Verify rendering in the owner's actual terminal and preserve the previous selection for reverting.
