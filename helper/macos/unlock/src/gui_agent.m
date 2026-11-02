#import <AppKit/AppKit.h>
#import <ApplicationServices/ApplicationServices.h>
#include <IOKit/pwr_mgt/IOPMLib.h>
#include "runtime.h"
#include <errno.h>
#include <signal.h>
#include <stdio.h>
#include <string.h>
#include <sys/socket.h>
#include <sys/stat.h>
#include <sys/un.h>
#include <unistd.h>

static volatile sig_atomic_t stopped;
static void stop_signal(int sig) { (void)sig; stopped = 1; }
static bool matches(const hu_gui_request *r, const hu_context *c) {
    return r->uid == c->uid && r->session[36] == 0 && r->boot[36] == 0 &&
        strcmp(r->session,c->session) == 0 && strcmp(r->boot,c->boot) == 0;
}
void hu_gui_handle(const hu_gui_request *r, hu_gui_response *out) {
    memset(out, 0, sizeof(*out));
    out->magic = HU_WIRE_MAGIC; out->version = 1;
    out->legacy_keychain = out->data_protection_keychain = INT32_MIN;
    if (r->magic != HU_WIRE_MAGIC || r->version != 1 || r->uid != getuid() ||
        r->operation < HU_GUI_STATUS || r->operation > HU_GUI_PROBE) { out->error = 1; return; }
    out->accessibility = AXIsProcessTrusted();
    uint32_t count = 0;
    if (CGGetActiveDisplayList(0,NULL,&count) == kCGErrorSuccess) out->displays = (int32_t)count;
    if (r->operation == HU_GUI_STATUS) return;
    hu_context current;
    if (!hu_macos_context(&current) || !matches(r,&current)) { out->error = 2; return; }
    if (r->operation == HU_GUI_PROBE) {
        if (current.locked) { out->error = 3; return; }
        out->legacy_keychain = hu_keychain_probe(false);
        out->data_protection_keychain = hu_keychain_probe(true);
        if (out->legacy_keychain || out->data_protection_keychain) out->error = 4;
        return;
    }
    if (!current.locked) { out->error = 3; return; }
    if (!out->accessibility || out->displays < 1) { out->error = 5; return; }
    IOPMAssertionID assertion = kIOPMNullAssertionID;
    IOPMAssertionDeclareUserActivity(CFSTR("Hermes authorized screen-unlock request"),
                                    kIOPMUserActiveLocal, &assertion);
    usleep(150000);
    if (!hu_macos_context(&current) || !current.locked || !matches(r,&current)) {
        if (assertion != kIOPMNullAssertionID) IOPMAssertionRelease(assertion);
        out->error = 2; return;
    }
    CGEventRef down = CGEventCreateKeyboardEvent(NULL, 36, true);
    CGEventRef up = CGEventCreateKeyboardEvent(NULL, 36, false);
    if (down && up) { CGEventPost(kCGHIDEventTap,down); CGEventPost(kCGHIDEventTap,up); }
    else out->error = 6;
    if (down) CFRelease(down);
    if (up) CFRelease(up);
    if (assertion != kIOPMNullAssertionID) IOPMAssertionRelease(assertion);
}

int hu_gui_serve(void) {
    uid_t allowed;
    if (!hu_allowed_uid(&allowed) || allowed != getuid() || getuid() == 0) return 0;
    struct sockaddr_un address = {.sun_family = AF_UNIX};
    if (!hu_gui_path(allowed,address.sun_path,sizeof(address.sun_path))) return 1;
    struct stat st;
    if (lstat(address.sun_path,&st) == 0) {
        if (!S_ISSOCK(st.st_mode) || st.st_uid != allowed) return 1;
        /* Only remove a stale socket; never steal an active agent's endpoint. */
        int probe = socket(AF_UNIX,SOCK_STREAM,0);
        if (probe < 0) return 1;
        int connected = connect(probe,(struct sockaddr *)&address,sizeof(address));
        int error = errno; close(probe);
        if (connected == 0) return 0;
        if (error != ECONNREFUSED) return 1;
        if (unlink(address.sun_path)) return 1;
    } else if (errno != ENOENT) return 1;
    int server = socket(AF_UNIX,SOCK_STREAM,0);
    if (server < 0) return 1;
    umask(0077);
    if (bind(server,(struct sockaddr *)&address,sizeof(address)) || listen(server,4)) { close(server); return 1; }
    signal(SIGTERM,stop_signal); signal(SIGINT,stop_signal);
    while (!stopped) {
        fd_set fds; FD_ZERO(&fds); FD_SET(server,&fds);
        struct timeval timeout = {.tv_sec=1};
        if (select(server+1,&fds,NULL,NULL,&timeout) <= 0) continue;
        int fd = accept(server,NULL,NULL);
        if (fd < 0) continue;
        hu_socket_timeout(fd);
        uid_t uid; gid_t gid;
        if (getpeereid(fd,&uid,&gid) == 0 && uid == 0) {
            hu_gui_request request; hu_gui_response response;
            if (hu_transfer(fd,&request,sizeof(request),false)) {
                hu_gui_handle(&request,&response);
                hu_transfer(fd,&response,sizeof(response),true);
            }
        }
        close(fd);
    }
    close(server); unlink(address.sun_path); return 0;
}

#ifndef HU_NO_AGENT_MAIN
int main(int argc,char **argv) {
    @autoreleasepool {
        if (argc == 2 && !strcmp(argv[1],"serve")) return hu_gui_serve();
        if (argc == 2 && !strcmp(argv[1],"permissions")) {
            printf("{\"accessibility\":%s}\n",AXIsProcessTrusted()?"true":"false"); return 0;
        }
        if (argc == 2 && !strcmp(argv[1],"request-permissions")) {
            NSDictionary *options = @{(__bridge NSString *)kAXTrustedCheckOptionPrompt:@YES};
            AXIsProcessTrustedWithOptions((__bridge CFDictionaryRef)options); return 0;
        }
        if (argc == 2 && !strcmp(argv[1],"probe")) {
            hu_context c;
            if (!hu_macos_context(&c) || c.locked || c.uid != getuid()) return 1;
            int legacy = hu_keychain_probe(false), dp = hu_keychain_probe(true);
            printf("{\"legacy_keychain_status\":%d,\"data_protection_status\":%d}\n",legacy,dp);
            return legacy || dp ? 1 : 0;
        }
        fputs("usage: hermes-unlock-agent serve|permissions|request-permissions|probe\n",stderr); return 2;
    }
}
#endif
