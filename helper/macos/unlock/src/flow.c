#include "flow.h"
#include <string.h>
static hu_flow_result fail(const char *reason) {
    return (hu_flow_result){.result=reason,.legacy=INT32_MIN,.data_protection=INT32_MIN};
}
static bool same(const hu_context *a,const hu_context *b) {
    return a->uid==b->uid && !strcmp(a->session,b->session) && !strcmp(a->boot,b->boot);
}
static hu_flow_result postcheck(uid_t uid, const hu_context *c, bool consumed, const hu_flow_ops *ops) {
    hu_gui_response r;
    if (!ops->gui(uid,HU_GUI_PROBE,c,&r)) return fail("agent_unavailable");
    hu_context after;
    if (!ops->context(&after) || !same(c,&after)) return fail("session_changed");
    if (after.locked) return fail("relocked");
    bool ready=!r.error && r.legacy_keychain==0 && r.data_protection_keychain==0 && r.displays>0;
    return (hu_flow_result){.result=ready?(consumed?"unlocked":"already_unlocked"):"postcheck_failed",
        .screen_unlocked=true,.consumed=consumed,.ready=ready,.legacy=r.legacy_keychain,
        .data_protection=r.data_protection_keychain,.displays=r.displays};
}
hu_flow_result hu_health_flow(uid_t uid,const hu_flow_ops *ops) {
    hu_context current;
    if (!ops->context(&current) || current.uid!=uid) return fail("wrong_console");
    if (current.locked) return fail("screen_locked");
    return postcheck(uid,&current,false,ops);
}
hu_flow_result hu_unlock_flow(uid_t uid,const hu_flow_ops *ops) {
    hu_context initial,issued,current;
    if (!ops->context(&initial) || initial.uid!=uid) return fail("wrong_console");
    if (!initial.locked) return postcheck(uid,&initial,false,ops);
    if (!ops->active()) return fail("not_active");
    hu_gui_response agent;
    if (!ops->gui(uid,HU_GUI_STATUS,NULL,&agent) || agent.error) return fail("agent_unavailable");
    if (!agent.accessibility || agent.displays<1) return fail("agent_not_ready");
    if (!ops->context(&issued) || !issued.locked || !same(&issued,&initial)) return fail("session_changed");
    hu_result armed=ops->arm(&issued);
    if (armed!=HU_OK) return fail(hu_result_name(armed));
    hu_flow_result result;
    if (!ops->gui(uid,HU_GUI_TRIGGER,&issued,&agent) || agent.error) {
        result=fail("trigger_failed"); goto cleanup;
    }
    for (int i=0;i<80;i++) {
        ops->wait();
        if (!ops->context(&current) || !same(&current,&issued)) { result=fail("session_changed"); goto cleanup; }
        if (!current.locked) {
            result=ops->consumed(&issued)?postcheck(uid,&current,true,ops):fail("unverified_unlock");
            goto cleanup;
        }
    }
    result=fail("unlock_timeout");
cleanup:
    ops->revoke(); return result;
}
