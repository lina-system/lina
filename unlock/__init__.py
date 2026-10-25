"""Hermes plugin: registration performs no network or system mutation."""
import json
import os
from .bridge.mac_unlock import invoke,command

def register(ctx):
    def configured():
        try:
            command(os.environ.get('HERMES_UNLOCK_SSH_HOST',''),'status')
            return True
        except ValueError:
            return False
    operations=[
        ('mac_unlock_status','status','Inspect the configured Mac unlock service. This does not unlock or install anything.'),
        ('mac_unlock','unlock','Request one screen unlock on the operator-configured Mac. Success requires the exact grant receipt, an unlocked session, both keychain probes and an active display. It does not validate FaceTime or reboot recovery.'),
        ('mac_unlock_revoke','revoke','Cancel any outstanding short-lived unlock authorization on the configured Mac.')]
    for name,operation,description in operations:
        def handle(params,_operation=operation,**kwargs):
            del kwargs
            if params:
                return json.dumps({'success':False,'result':'unexpected_arguments','desktop_ready':False})
            try:
                code,result=invoke(os.environ.get('HERMES_UNLOCK_SSH_HOST',''),_operation)
            except (ValueError,OSError):
                return json.dumps({'success':False,'result':'not_configured','desktop_ready':False})
            return json.dumps({**result,'success':code==0},sort_keys=True)
        ctx.register_tool(name=name,toolset='mac_unlock',schema={'name':name,'description':description,
            'parameters':{'type':'object','properties':{},'additionalProperties':False}},handler=handle,check_fn=configured)
