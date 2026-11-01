"""Hermes plugin ``lina-helper``: choose which Mac computer_use acts on, and light checks by Mac name.

Screen reading and input are Hermes ``computer_use`` (backend ``lina-helper-cua``). Registration reads
macs.yaml only; it never touches the network.
"""
from . import core, macs

TOOLSET = 'lina_helper'


def _schemas(names):
    mac = {'type': 'string', 'description': 'Mac name.'}
    if names:
        mac['enum'] = names
    optional_mac = dict(mac, description='Mac name. Default: the current computer_use target.')
    spec = {
        'mac_use': (
            "Point this session's computer_use tool at a Mac. Default target is agent-hub. Changing it restarts the "
            "session's computer_use connection on the new Mac; capture again afterwards.",
            {'mac': mac}, ['mac']),
        'mac_status': (
            'LINA Helper on a Mac: version, permission grants, policy profile, cua-driver state and who holds the input '
            "lease. Also reports this session's computer_use target.",
            {'mac': optional_mac}, []),
        'mac_screenshot': (
            'Quick full-screen screenshot of a Mac through LINA Helper (no computer_use session). Returns a local PNG '
            'path with width, height and bytes. To act on the screen use computer_use.',
            {'mac': optional_mac,
             'display': {'type': ['string', 'integer'], 'description': '"main" (default) or a display id.'}}, []),
    }
    for name, (description, props, required) in spec.items():
        yield name, {'name': name, 'description': description,
                     'parameters': {'type': 'object', 'properties': props, 'required': required,
                                    'additionalProperties': False}}


def register(ctx):
    config = macs.load()
    names = macs.names(config) or [core.DEFAULT_MAC]
    dispatcher = core.Dispatcher()
    for name, schema in _schemas(names):
        def handle(params, _name=name, **kwargs):
            del kwargs
            return dispatcher.call(_name, params)
        ctx.register_tool(name=name, toolset=TOOLSET, schema=schema, handler=handle)
