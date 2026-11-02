#include "flow.h"
#include <assert.h>
#include <stdio.h>
#include <string.h>
static hu_context state;
static int arms,triggers,revokes,ticks,probes;
static bool active_flag,receipt,trigger_ok,stay_locked,change_session,agent_ok,ax,dp_ok,relock;
static bool context(hu_context *c) { *c=state; return true; }
static bool active(void) { return active_flag; }
static hu_result arm(const hu_context *c) { assert(c->locked); arms++; return HU_OK; }
static bool used(const hu_context *c) { (void)c; return receipt; }
static void revoke(void) { revokes++; }
static void wait_tick(void) { ticks++; if (change_session) state.uid++; else if (!stay_locked) state.locked=false; }
static bool gui(uid_t uid,unsigned op,const hu_context *c,hu_gui_response *r) {
    (void)uid; (void)c; memset(r,0,sizeof(*r)); r->displays=1; r->accessibility=ax;
    if (!agent_ok) return false;
    if (op==HU_GUI_TRIGGER) { triggers++; r->error=trigger_ok?0:1; }
    if (op==HU_GUI_PROBE) { probes++; r->data_protection_keychain=dp_ok?0:-25308; if (relock) state.locked=true; }
    return true;
}
static const hu_flow_ops ops={context,active,gui,arm,used,revoke,wait_tick};
static void reset(void) {
    state=(hu_context){.uid=501,.locked=true};
    strcpy(state.session,"11111111-1111-1111-1111-111111111111");
    strcpy(state.boot,"22222222-2222-2222-2222-222222222222");
    arms=triggers=revokes=ticks=probes=0;
    active_flag=receipt=trigger_ok=agent_ok=ax=dp_ok=true;
    stay_locked=change_session=relock=false;
}
static void expect(const char *s,bool ready) {
    hu_flow_result r=hu_unlock_flow(501,&ops);
    assert(!strcmp(r.result,s)); assert(r.ready==ready);
}
int main(void) {
    reset(); expect("unlocked",true); assert(arms==1 && triggers==1 && revokes==1 && probes==1);
    reset(); receipt=false; expect("unverified_unlock",false); assert(probes==0 && revokes==1);
    reset(); dp_ok=false; expect("postcheck_failed",false); assert(revokes==1);
    reset(); change_session=true; expect("session_changed",false); assert(revokes==1 && probes==0);
    reset(); trigger_ok=false; expect("trigger_failed",false); assert(revokes==1 && ticks==0);
    reset(); stay_locked=true; expect("unlock_timeout",false); assert(ticks==80 && arms==1 && triggers==1 && revokes==1);
    reset(); ax=false; expect("agent_not_ready",false); assert(arms==0);
    reset(); agent_ok=false; expect("agent_unavailable",false); assert(arms==0);
    reset(); active_flag=false; expect("not_active",false); assert(arms==0);
    reset(); state.locked=false; expect("already_unlocked",true); assert(arms==0 && triggers==0 && probes==1);
    reset(); relock=true; expect("relocked",false); assert(revokes==1);
    reset(); hu_flow_result health=hu_health_flow(501,&ops);
    assert(!strcmp(health.result,"screen_locked") && !health.ready && arms==0 && triggers==0);
    puts("PASS synthetic request -> trigger -> observed unlock -> matching receipt -> both keychain probes");
    puts("PASS no receipt, keychain failure, session switch, relock, timeout and trigger failures never report ready");
    puts("PASS one arm and trigger per request; all armed outcomes revoke; already-unlocked does not inject input");
}
