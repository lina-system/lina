"""Hermes plugin: operate agent Macs through LINA Helper by name. Registration does no I/O beyond reading macs.yaml."""
from . import core, macs

TOOLSET = 'lina_helper'

_MAC = 'Name of the Mac to act on.'
_APP = 'Bundle ID of the app, or "frontmost".'


def _schemas(names):
    mac = {'type': 'string', 'description': _MAC}
    if names:
        mac['enum'] = names
    spec = {
        'mac_status': ('Report the LINA Helper version, uptime and permission grants on a Mac.', {}, []),
        'mac_screenshot': (
            'Take a screenshot of a Mac. Returns a local PNG path (show it to the user) with width, height and bytes.',
            {'display': {'type': ['string', 'integer'], 'description': '"main" (default) or a display id.'}}, []),
        'mac_ax_tree': (
            'Read the accessibility tree of an app on a Mac: elements with stable paths, role, title, value, frame.',
            {'app': {'type': 'string', 'description': _APP + ' Default: frontmost.'},
             'depth': {'type': 'integer', 'minimum': 1, 'maximum': 8},
             'max_nodes': {'type': 'integer', 'minimum': 1, 'maximum': 2000}}, []),
        'mac_ax_press': (
            'Press an accessibility element, addressed by a path from mac_ax_tree.',
            {'app': {'type': 'string', 'description': _APP},
             'path': {'type': 'string', 'description': 'Element path from mac_ax_tree.'},
             'expect_role': {'type': 'string', 'description': 'Refuse unless the element has this role.'}},
            ['app', 'path']),
        'mac_click': (
            'Click at global screen coordinates on a Mac.',
            {'x': {'type': 'number'}, 'y': {'type': 'number'},
             'button': {'type': 'string', 'description': 'left (default) or right.'}}, ['x', 'y']),
        'mac_keys': (
            'Type text, or press one key with modifiers, on a Mac. Give exactly one of text or key.',
            {'text': {'type': 'string', 'maxLength': 512}, 'key': {'type': 'string'},
             'modifiers': {'type': 'array', 'items': {'type': 'string'}, 'description': 'Only with key.'}}, []),
        'mac_cua': (
            'Call one cua-driver tool on a Mac by name with a JSON arguments object.',
            {'tool': {'type': 'string', 'pattern': '^[a-z_]{1,64}$', 'description': 'cua-driver tool name.'},
             'args': {'type': 'object', 'description': 'Arguments for that tool.'}}, ['tool']),
    }
    for name, (description, props, required) in spec.items():
        yield name, {
            'name': name, 'description': description,
            'parameters': {'type': 'object', 'properties': dict(props, mac=mac),
                           'required': ['mac'] + required, 'additionalProperties': False}}


def register(ctx):
    config = macs.load()
    dispatcher = core.Dispatcher()
    for name, schema in _schemas(macs.names(config)):
        def handle(params, _name=name, **kwargs):
            del kwargs
            return dispatcher.call(_name, params)
        ctx.register_tool(name=name, toolset=TOOLSET, schema=schema, handler=handle)
