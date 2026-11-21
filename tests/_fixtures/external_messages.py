"""External-message spool tests have no messenger or HTTP dependencies."""

import importlib
import socket

import pytest


@pytest.fixture(autouse=True)
def _offline_external_messages(request):
    if request.path.stem.startswith("test_external_messages"):
        request.getfixturevalue("offline_messenger_clients")


@pytest.fixture
def offline_messenger_clients(monkeypatch):
    def refused(*args, **kwargs):
        raise RuntimeError("external-message tests must inject a fake messenger client")

    # Patch the classes, including references imported by adapters at module load.
    for module, clients in (
        ("httpx", ("Client", "AsyncClient")),
        ("requests.sessions", ("Session",)),
        ("aiohttp", ("ClientSession",)),
        ("imaplib", ("IMAP4",)),
        ("smtplib", ("SMTP",)),
        ("slack_sdk.web.base_client", ("BaseClient",)),
        ("slack_sdk.web.async_base_client", ("AsyncBaseClient",)),
        ("discord", ("Client",)),
        ("telegram", ("Bot",)),
    ):
        try:
            sdk = importlib.import_module(module)
        except ModuleNotFoundError as exc:
            if exc.name == module.split(".")[0]:
                continue
            raise
        for name in clients:
            monkeypatch.setattr(getattr(sdk, name), "__init__", refused)

    for name in ("getaddrinfo", "gethostbyname", "gethostbyname_ex", "gethostbyaddr"):
        monkeypatch.setattr(socket, name, refused)
    for name in ("connect", "connect_ex", "sendto"):
        monkeypatch.setattr(socket.socket, name, refused)
