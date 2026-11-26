"""Observe the parity runner's verdict after a child ignores a connect error."""

import errno
import socket
import subprocess
import sys
from pathlib import Path

import pytest

from tests.tui_gateway._parity_network_guard import build_network_guard, guarded_arm

pytestmark = pytest.mark.platforms("linux")


@pytest.fixture(scope="module")
def library(tmp_path_factory: pytest.TempPathFactory) -> Path:
    return build_network_guard(tmp_path_factory.mktemp("network-guard"))


@pytest.mark.parametrize("child", ["python", "native"])
def test_swallowed_connect_fails_arm(child: str, library: Path, tmp_path: Path) -> None:
    # A listening TCP socket cannot initiate a connection, even on the unguarded base.
    if child == "python":
        command = [sys.executable, "-I", "-c", """
import socket
with socket.socket() as sock:
    sock.listen()
    print(sock.connect_ex(("192.0.2.1", 9)))
"""]
    else:
        probe = tmp_path / "probe"
        subprocess.run(["cc", "-x", "c", "-", "-o", str(probe)], check=True, text=True, input="""
#include <arpa/inet.h>
#include <errno.h>
#include <stdio.h>
#include <sys/socket.h>
int main(void) {
    struct sockaddr_in addr = {.sin_family = AF_INET, .sin_port = htons(9)};
    inet_pton(AF_INET, "192.0.2.1", &addr.sin_addr);
    int fd = socket(AF_INET, SOCK_STREAM, 0);
    if (listen(fd, 1)) return 1;
    int rc = connect(fd, (struct sockaddr *)&addr, sizeof(addr));
    printf("%d\\n", rc < 0 ? errno : 0);
    return 0;
}
""")
        command = [str(probe)]
    with pytest.raises(AssertionError, match=r"192\.0\.2\.1:9"):
        with guarded_arm(library, tmp_path, {}) as env:
            result = subprocess.run(command, env=env, check=True, capture_output=True, text=True)
    assert int(result.stdout) == errno.EACCES


@pytest.mark.parametrize("family", [socket.AF_INET, socket.AF_INET6, socket.AF_UNIX])
def test_local_connection_delivers_bytes(family: socket.AddressFamily, library: Path, tmp_path: Path) -> None:
    # Linux abstract sockets avoid the sun_path limit in deeply nested checkouts.
    with socket.socket(family) as listener:
        listener.bind({socket.AF_INET: ("127.4.3.2", 0), socket.AF_INET6: ("::1", 0),
                       socket.AF_UNIX: b""}[family])
        listener.listen()
        address = listener.getsockname()
        script = f"""
import socket
with socket.socket({int(family)}) as sock:
    sock.connect({address!r})
    sock.sendall(b'local')
"""
        with guarded_arm(library, tmp_path, {}) as env:
            subprocess.run([sys.executable, "-I", "-c", script], env=env, check=True)
        conn, _ = listener.accept()
        with conn:
            assert conn.recv(5) == b"local"
