"""lina#244: the Web Push message itself (RFC 8291, RFC 8292) and ``/api/lina/push/*`` over a scratch LINA root, the
served JSON validated against ``docs/api/push-client.yaml``."""

from __future__ import annotations

import stat

import jwt
import pytest
from cryptography.hazmat.primitives.asymmetric import ec
from fastapi.testclient import TestClient

from hermes_cli import lina_ocx, lina_push
from hermes_cli.lina_push import b64u, unb64u
from tests.hermes_cli._lina_push_fakes import Device, PushService, throwaway_key, valid
from tests.hermes_cli._work_env import NOW, WorkEnv

pytestmark = pytest.mark.platforms("posix")


@pytest.fixture
def work(tmp_path, monkeypatch):
    env = WorkEnv(tmp_path, monkeypatch)
    monkeypatch.setattr(lina_ocx, "lina_root", lambda: env.root)
    monkeypatch.delenv(lina_push.KEY_ENV, raising=False)
    monkeypatch.delenv(lina_push.SUBJECT_ENV, raising=False)
    return env


@pytest.fixture
def key(monkeypatch):
    value = throwaway_key()
    monkeypatch.setenv(lina_push.KEY_ENV, value)
    return value


@pytest.fixture
def client():
    from hermes_cli import web_server
    test_client = TestClient(web_server.app)
    test_client.headers[web_server._SESSION_HEADER_NAME] = web_server._SESSION_TOKEN
    return test_client


def test_rfc8291_appendix_a_vector_reproduces_byte_for_byte():
    as_private = ec.derive_private_key(int.from_bytes(unb64u("yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw"), "big"),
                                       ec.SECP256R1())
    body = lina_push.encrypt(
        unb64u("V2hlbiBJIGdyb3cgdXAsIEkgd2FudCB0byBiZSBhIHdhdGVybWVsb24"),
        unb64u("BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4"),
        unb64u("BTBZMqHH6r4Tts7J_aSIgg"), as_private=as_private, salt=unb64u("DGv6ra1nlYgDCS1FRnbzlw"))
    assert b64u(body) == ("DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6Tl"
                          "zAC8wEqKK6PBru3jl7A_yl95bQpu6cVPTpK4Mqgkf1CXztLVBSt2Ks3oZwbuwXPXLWyouBWLVWGNWQexSgSxsj_Qulcy4a-fN")


def test_a_sent_message_carries_vapid_claims_for_the_endpoint_origin_and_decrypts_on_the_device(work, key, monkeypatch):
    monkeypatch.setenv(lina_push.SUBJECT_ENV, "mailto:owner@example.org")
    vapid, _ = lina_push.vapid_key()
    device = Device("one")
    service = PushService(device)
    status = lina_push.send(vapid, device.subscription(), {"v": 1, "type": "close", "tag": "ask:T1:1"}, service.post,
                            now=NOW)
    assert status == 201 and device.got == [{"v": 1, "type": "close", "tag": "ask:T1:1"}]
    headers = service.headers[0]
    token, k = headers["Authorization"].removeprefix("vapid t=").split(", k=")
    assert k == vapid.public and len(unb64u(k)) == 65
    public = ec.EllipticCurvePublicKey.from_encoded_point(ec.SECP256R1(), unb64u(k))
    claims = jwt.decode(token, public, algorithms=["ES256"], audience="https://fcm.googleapis.com",
                        options={"verify_exp": False})
    assert claims == {"aud": "https://fcm.googleapis.com", "exp": claims["exp"], "sub": "mailto:owner@example.org"}
    assert NOW < claims["exp"] <= NOW + 24 * 3600
    assert headers["Content-Encoding"] == "aes128gcm" and len(headers["Topic"]) <= 32


def test_without_a_key_push_is_off_the_route_says_so_and_subscribing_is_refused(work, client):
    key = valid("PushKey", client.get("/api/lina/push/key").json())
    assert key == {"version": 1, "enabled": False, "publicKey": None}
    refused = client.post("/api/lina/push/subscriptions", json=Device("one").subscription())
    assert refused.status_code == 503 and valid("Error", refused.json())
    assert not (work.root / "push").exists()


@pytest.mark.parametrize("bad", ["", "not base64!", b64u(b"\x00" * 32), b64u(b"\x01" * 31)])
def test_a_key_that_is_not_a_raw_p256_private_key_leaves_push_off(work, monkeypatch, bad):
    monkeypatch.setenv(lina_push.KEY_ENV, bad)
    vapid, why = lina_push.vapid_key()
    assert vapid is None and why and (not bad or bad not in why)


def test_a_subscription_is_stored_once_per_endpoint_with_mode_600_and_removed(work, key, client):
    device = Device("one")
    first = valid("SubscriptionResult", client.post("/api/lina/push/subscriptions", json=device.subscription()).json())
    again = valid("SubscriptionResult", client.post("/api/lina/push/subscriptions",
                                                   json=device.subscription(expirationTime=None)).json())
    assert first["created"] is True and again == dict(first, created=False)
    files = list((work.root / "push" / "subscriptions").iterdir())
    assert [f.stem for f in files] == [first["id"]]
    assert stat.S_IMODE(files[0].stat().st_mode) == 0o600
    assert stat.S_IMODE((work.root / "push").stat().st_mode) == 0o700
    public = valid("PushKey", client.get("/api/lina/push/key").json())
    assert public["enabled"] is True and public["publicKey"] == lina_push.vapid_key()[0].public
    removed = client.post("/api/lina/push/subscriptions/remove", json={"endpoint": device.endpoint})
    assert valid("RemoveResult", removed.json()) == {"ok": True, "removed": True}
    assert valid("RemoveResult", client.post("/api/lina/push/subscriptions/remove",
                                             json={"endpoint": device.endpoint}).json())["removed"] is False
    assert not any((work.root / "push" / "subscriptions").iterdir())


@pytest.mark.parametrize("endpoint", [
    "http://fcm.googleapis.com/fcm/send/x",  # not https
    "https://example.org/push/x",  # not a push service
    "https://fcm.googleapis.com.evil.example/x",  # a look-alike host
    "https://evilpush.apple.com/x",  # a suffix without its dot
    "https://fcm.googleapis.com:8443/x",  # another port
    "https://user@fcm.googleapis.com/x",  # credentials in the URL
    "https://127.0.0.1/x",
])
def test_an_endpoint_that_is_not_a_known_push_service_over_https_is_refused(work, key, client, endpoint):
    body = Device("one").subscription()
    body["endpoint"] = endpoint
    refused = client.post("/api/lina/push/subscriptions", json=body)
    assert refused.status_code == 400 and valid("Error", refused.json())
    assert not (work.root / "push" / "subscriptions").exists()


@pytest.mark.parametrize("host", ["web.push.apple.com", "updates.push.services.mozilla.com",
                                  "wns2-par02p.notify.windows.com", "fcm.googleapis.com"])
def test_the_four_push_services_are_accepted(work, key, client, host):
    assert client.post("/api/lina/push/subscriptions", json=Device("one", host).subscription()).status_code == 200


def test_bad_keys_are_refused(work, key, client):
    body = Device("one").subscription()
    body["keys"]["p256dh"] = b64u(b"\x04" + b"\x01" * 64)
    assert client.post("/api/lina/push/subscriptions", json=body).status_code == 400
    body = Device("one").subscription()
    body["keys"]["auth"] = b64u(b"short")
    assert client.post("/api/lina/push/subscriptions", json=body).status_code == 400


def test_an_expired_subscription_is_forgotten_and_never_sent_to(work, key):
    expired, alive = Device("old"), Device("new")
    lina_push.save_subscription(work.root, expired.subscription(expirationTime=int(NOW * 1000) - 1))
    lina_push.save_subscription(work.root, alive.subscription(expirationTime=int(NOW * 1000) + 60_000))
    subs = lina_push.subscriptions(work.root, NOW)
    assert [s["endpoint"] for s in subs.values()] == [alive.endpoint]
    assert [f.stem for f in (work.root / "push" / "subscriptions").iterdir()] == list(subs)
