# Shared design tokens

Core owns the canonical DTCG source. `light.json`, `dark.json` and
`high-contrast.json` hold theme values; `app.json` holds common layout and type
roles plus the app's word-wrap and spring extensions. `icons.json` holds the
shared icon geometry and Unicode/ASCII equivalents. The spring and word-wrap
extensions remain app metadata; the terminal does not approximate spring motion.

The reconciliation imports [app PR #101](https://github.com/lina-system/lina-app/pull/101)
at `69d2a324ab09d0ad9405b78bb782cffe24e2c2a9`, whose pinned Core baseline was
`83de7a325186e0c16c8fcbaf768e4455ccb5ad0b`. It was compared with Core dev
`a3ede8487bc0508ac689cb5dedd0d5ab9fb76351`.
[app-source.json](app-source.json) records the original app input hashes, not
the hashes of the reconciled files. The app PR was open at import time; this
does not claim its Flutter work is merged or verified by Core.

Existing Core values and terminal metadata are retained. Added app colors keep
their RGB values and aliases, with explicit 16/256-color metadata. Aliases inherit
terminal metadata unless they override it. `onSelection` overrides it because
the available terminal selection backgrounds need a different foreground.
The app's compact spacing gains a one-cell terminal fallback. Shared icons use
`glyph.unicode.icon*` and `glyph.ascii.icon*` roles so existing connection and
stage glyphs keep their meanings. Tool rows consume the shared working, success
and error icons. Unknown icon names return the visible unknown mark. These
generic marks do not settle the agent/persona exploration in app issue #4.

## Updating both repositories

1. Record the exact Core base and app source commits before comparing changes.
   Fetch files by immutable commit, including the app's `upstream.json` and
   `design/tokens/core/` baseline; do not copy a moving branch over Core's source.
2. Compare the app additions with its pinned baseline and current Core. Preserve
   newer Core roles and terminal metadata, resolve any conflicting values in the
   source PR, and update `app-source.json` with the inspected app commit and
   original input hashes. Common roles belong in `app.json`; theme colors belong
   in each theme. Icon mappings and geometry change together in `icons.json`.
   Add the role to the generator schema and tests when extending the role set.
3. Run `node scripts/tokens.mjs`, `node scripts/tokens.mjs --check`,
   `node --test scripts/tokens.test.mjs` and the focused TUI tests. Include the
   generated Go, CSS, Swift and DESIGN table changes in the same Core PR. Verify
   the changed range through foundation and exercise the installed Core/TUI path.
4. After Core merges, open a separate app update pinned to that exact Core
   commit. Refresh `design/tokens/core/`, its `upstream.json` hashes, the common
   tokens and icon source. Remove local copies of extensions now supplied by
   Core instead of applying them twice. Adapt the app's loader to the reconciled
   shared files, regenerate its Dart theme and DESIGN tables, and run the app
   generation, contrast, lint and Flutter gates. Record both revisions there.
5. A later app proposal repeats this procedure. Neither repository's passing
   tests substitute for the other's verification. Do not automatically sync
   branches or edit generated files to resolve drift.

## Checks and terminal fallbacks

The loader resolves typed aliases, rejects missing/cyclic references and duplicate
shared roles, and validates every theme's complete role set. Every color has both
terminal indexes. Text and semantic status colors meet 4.5:1 on canvas and surface
in RGB and the standard terminal palettes. Diff and selected text meet 4.5:1 on
their paired backgrounds; the selection fill is distinguishable from canvas and
surface at 3:1. Existing launcher text/accent high-contrast roles retain 7:1.
User-customized ANSI palettes can change physical contrast.

`ui.NewStyles` maps these values to Lip Gloss styles for truecolor, ANSI256,
ANSI16 and monochrome. Status words, unknown labels and diff signs remain the
meaning; color is supplementary. `NO_COLOR` removes styling independently of
the Unicode/ASCII preference. The icon source rejects missing mappings, control
characters, private-use glyphs and non-ASCII fallback strings.

The Lucide subset retains its [ISC notice](LUCIDE-LICENSE.txt) and the inherited
Feather [MIT notice](FEATHER-LICENSE.txt). LINA's custom geometry is Apache-2.0.
