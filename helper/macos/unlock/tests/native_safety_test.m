#include "runtime.h"
#include <assert.h>
#include <limits.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/socket.h>
#include <unistd.h>

int main(int argc,char **argv) {
    assert(argc==1);
    hu_gui_request request={.magic=HU_WIRE_MAGIC,.version=1,.operation=HU_GUI_TRIGGER,.uid=getuid()};
    hu_gui_response response;
    request.version=999; hu_gui_handle(&request,&response); assert(response.error==1);
    request.version=1; request.uid++; hu_gui_handle(&request,&response); assert(response.error==1);
    request.uid--; request.operation=999; hu_gui_handle(&request,&response); assert(response.error==1);
    request.operation=HU_GUI_TRIGGER; hu_gui_handle(&request,&response); assert(response.error==2);
    request.operation=HU_GUI_PROBE; hu_gui_handle(&request,&response); assert(response.error==2);
    puts("PASS invalid GUI version, user, operation and missing session reject before input or keychain access");
    int sockets[2]; assert(socketpair(AF_UNIX,SOCK_STREAM,0,sockets)==0);
    hu_socket_timeout(sockets[0]); hu_socket_timeout(sockets[1]);
    char path[PATH_MAX]; assert(realpath(argv[0],path));
    assert(hu_peer_code_matches(sockets[0],getuid(),path));
    assert(!hu_peer_code_matches(sockets[0],getuid()+1,path));
    assert(!hu_peer_code_matches(sockets[0],getuid(),"/bin/ls"));
    char sent[]="bounded-wire",received[sizeof(sent)]={0};
    assert(hu_transfer(sockets[0],sent,sizeof(sent),true));
    assert(hu_transfer(sockets[1],received,sizeof(received),false));
    assert(!memcmp(sent,received,sizeof(sent)));
    close(sockets[0]);close(sockets[1]);
    puts("PASS kernel peer UID and audit-token code hash validation; wire framing round-trip");
}
