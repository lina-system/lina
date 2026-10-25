#include "flow.h"
#include <Security/AuthorizationDB.h>
#include <fcntl.h>
#include <stdio.h>
#include <string.h>
#include <sys/file.h>
#include <sys/stat.h>
#include <unistd.h>

static int failure(const char *reason) {
    printf("{\"schema_version\":1,\"result\":\"%s\",\"desktop_ready\":false}\n",reason); return 1;
}
static bool active(void) {
    CFDictionaryRef right = NULL;
    if (AuthorizationRightGet("system.login.screensaver",&right) != errAuthorizationSuccess) return false;
    CFTypeRef rules = CFDictionaryGetValue(right,CFSTR("rule"));
    bool ok = rules && CFGetTypeID(rules) == CFArrayGetTypeID() && CFArrayGetCount(rules) == 2 &&
        CFEqual(CFArrayGetValueAtIndex(rules,0),CFSTR("local.hermes.unlock.remote")) &&
        CFEqual(CFArrayGetValueAtIndex(rules,1),CFSTR("local.hermes.unlock.original"));
    CFRelease(right); return ok;
}
static int request_lock(void) {
    int fd = open(HU_DIRECTORY "/request.lock",O_RDWR|O_CREAT|O_NOFOLLOW|O_CLOEXEC,0600);
    if (fd < 0) return -1;
    struct stat st;
    if (fstat(fd,&st) || !S_ISREG(st.st_mode) || st.st_uid != 0 || st.st_nlink != 1 ||
        (st.st_mode & 0777) != 0600 || flock(fd,LOCK_EX|LOCK_NB)) { close(fd); return -1; }
    return fd;
}
static hu_result arm(const hu_context *c) { return hu_arm(HU_DIRECTORY,0,c,UINT64_C(5000000000)); }
static bool used(const hu_context *c) { return hu_consumed(HU_DIRECTORY,0,c); }
static void revoke_grant(void) { (void)hu_revoke(HU_DIRECTORY,0); }
static void wait_tick(void) { usleep(100000); }
int main(int argc, char **argv) {
    if (argc != 2) { fputs("usage: hermes-unlockctl status|health|unlock|revoke|prepare-runtime\n",stderr); return 2; }
    const char *op = argv[1];
    uid_t uid = 0; bool installed = hu_allowed_uid(&uid);
    hu_context current; bool known = hu_macos_context(&current);
    if (!strcmp(op,"status")) {
        hu_gui_response agent = {0};
        bool connected = installed && geteuid() == 0 && hu_exchange(uid,HU_GUI_STATUS,NULL,&agent);
        printf("{\"schema_version\":1,\"result\":\"status\",\"installed\":%s,\"active\":%s,"
            "\"console_known\":%s,\"screen_locked\":%s,\"agent_connected\":%s,\"accessibility\":%s,"
            "\"active_displays\":%d,\"desktop_ready\":false}\n",installed?"true":"false",active()?"true":"false",
            known?"true":"false",!known?"null":(current.locked?"true":"false"),connected?"true":"false",
            connected&&agent.accessibility?"true":"false",connected?agent.displays:0);
        return 0;
    }
    if (strcmp(op,"unlock") && strcmp(op,"health") && strcmp(op,"revoke") && strcmp(op,"prepare-runtime")) return failure("unknown_command");
    if (geteuid() != 0) return failure("requires_root");
    if (!installed) return failure("not_installed");
    if (!hu_prepare_runtime(uid)) return failure("runtime_unavailable");
    if (!strcmp(op,"prepare-runtime")) { puts("{\"schema_version\":1,\"result\":\"prepared\"}"); return 0; }
    if (!strcmp(op,"revoke")) {
        hu_result r = hu_revoke(HU_DIRECTORY,0);
        if (r != HU_OK && r != HU_UNAVAILABLE) return failure(hu_result_name(r));
        puts("{\"schema_version\":1,\"result\":\"revoked\",\"desktop_ready\":false}"); return 0;
    }
    if (!known || current.uid != uid) return failure("wrong_console");
    int lock = request_lock();
    if (lock < 0) return failure("request_busy");
    const hu_flow_ops ops={hu_macos_context,active,hu_exchange,arm,used,revoke_grant,wait_tick};
    hu_flow_result result=!strcmp(op,"health")?hu_health_flow(uid,&ops):hu_unlock_flow(uid,&ops);
    printf("{\"schema_version\":1,\"result\":\"%s\",\"screen_unlocked\":%s,"
        "\"authorization_consumed\":%s,\"legacy_keychain_status\":%d,\"data_protection_status\":%d,"
        "\"active_displays\":%d,\"desktop_ready\":%s,\"facetime_verified\":false}\n",
        result.result,result.screen_unlocked?"true":"false",result.consumed?"true":"false",
        result.legacy,result.data_protection,result.displays,result.ready?"true":"false");
    close(lock); return result.ready?0:1;
}
