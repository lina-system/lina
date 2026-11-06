"""Shared pieces of the lina#244 push tests: a throwaway VAPID key, fake devices that decrypt what they receive (the
receiver side of RFC 8291), and the push contract as JSON Schema."""

from __future__ import annotations

import hashlib
import hmac
import json
import struct
from pathlib import Path

import jsonschema
from cryptography.hazmat.primitives.asymmetric import ec
from cryptography.hazmat.primitives.ciphers.aead import AESGCM

import hermes_yaml
from hermes_cli.lina_push import _public_bytes, b64u

REPO = Path(__file__).resolve().parents[2]


def _openapi_to_jsonschema(node):
    if isinstance(node, list):
        return [_openapi_to_jsonschema(n) for n in node]
    if not isinstance(node, dict):
        return node
    out = {k: _openapi_to_jsonschema(v) for k, v in node.items() if k != "nullable"}
    if node.get("nullable") and "type" in out:
        out["type"] = [out["type"], "null"]
    return out


COMPONENTS = _openapi_to_jsonschema(
    hermes_yaml.safe_load((REPO / "docs/api/push-client.yaml").read_text(encoding="utf-8-sig"))["components"])


def valid(schema: str, data):
    jsonschema.Draft7Validator({"$ref": f"#/components/schemas/{schema}", "components": COMPONENTS}).validate(data)
    return data


def throwaway_key() -> str:
    """A VAPID private key made in memory for one test, as the environment carries it."""
    return b64u(ec.generate_private_key(ec.SECP256R1()).private_numbers().private_value.to_bytes(32, "big"))


def _hmac(key: bytes, data: bytes) -> bytes:
    return hmac.new(key, data, hashlib.sha256).digest()


def decrypt(body: bytes, ua_private: ec.EllipticCurvePrivateKey, auth: bytes) -> bytes:
    salt, (rs, idlen) = body[:16], struct.unpack("!IB", body[16:21])
    as_public, ciphertext = body[21:21 + idlen], body[21 + idlen:]
    assert rs == 4096 and len(ciphertext) <= rs
    ua_public = _public_bytes(ua_private.public_key())
    secret = ua_private.exchange(ec.ECDH(), ec.EllipticCurvePublicKey.from_encoded_point(ec.SECP256R1(), as_public))
    ikm = _hmac(_hmac(auth, secret), b"WebPush: info\x00" + ua_public + as_public + b"\x01")
    prk = _hmac(salt, ikm)
    cek, nonce = _hmac(prk, b"Content-Encoding: aes128gcm\x00\x01")[:16], _hmac(prk, b"Content-Encoding: nonce\x00\x01")[:12]
    plain = AESGCM(cek).decrypt(nonce, ciphertext, None)
    assert plain.endswith(b"\x02")
    return plain[:-1]


class Device:
    """One browser subscription: its keys, and the messages the push service took for it."""

    def __init__(self, name: str, host: str = "fcm.googleapis.com"):
        self.private = ec.generate_private_key(ec.SECP256R1())
        self.auth = hashlib.sha256(name.encode()).digest()[:16]
        self.endpoint = f"https://{host}/fcm/send/{name}"
        self.got: list[dict] = []

    def subscription(self, **extra) -> dict:
        return {"endpoint": self.endpoint, "keys": {"p256dh": b64u(_public_bytes(self.private.public_key())),
                                                    "auth": b64u(self.auth)}, **extra}


class PushService:
    """The HTTP seam: decrypts each post for the device it names and answers the status set for it."""

    def __init__(self, *devices: Device):
        self.devices = {d.endpoint: d for d in devices}
        self.status: dict[str, int] = {}
        self.headers: list[dict] = []

    def post(self, url: str, headers: dict, body: bytes) -> int:
        self.headers.append(headers)
        status = self.status.get(url, 201)
        if status < 300:
            device = self.devices[url]
            device.got.append(valid("PushMessage", json.loads(decrypt(body, device.private, device.auth))))
        return status
