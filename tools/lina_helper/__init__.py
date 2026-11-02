"""LINA Helper: the signed macOS app that holds a Mac's privacy grants (``helper/macos/``).

The core reaches it with ``lina-helperctl`` on the Mac LINA runs on, or over SSH on another Mac named in
``devices.macs`` (config.yaml). ``tool.py`` registers the model tools; ``computer_use`` reaches the helper
through the ``lina_helper`` computer-use provider (``plugins/computer_use/lina_helper``).
"""
