"""Web Push to the owner's devices (lina#244): the VAPID key, the subscriptions and one encrypted message.

- The key: ``LINA_PUSH_VAPID_PRIVATE_KEY`` (base64url of the raw 32-byte P-256 private key), read from the environment
  only (``.env`` holds secrets); the subject is the setting ``lina.push.subject`` in config.yaml (``mailto:`` or
  ``https:``; default the project URL). Without a usable key push is off. The key is never written, logged or
  returned; only its public half leaves this module.
- Subscriptions: one file per device in ``$LINA_HOME/push/subscriptions/<id>.json`` (mode 600), the id a hash of the
  endpoint, so adding the same endpoint again refreshes it. An endpoint must be ``https`` on a known push service host,
  so a client cannot make the core post to an address of its choosing.
- A message: the payload encrypted per RFC 8291 (``aes128gcm``, one record) and signed per RFC 8292 (an ES256 JWT with
  ``aud`` = the endpoint's origin). ``Topic`` is a hash of the notification tag, so the push service also replaces a
  message for the same item that has not been delivered yet.
"""

from __future__ import annotations

import base64
import hashlib
import hmac
import json
import logging
import os
import struct
import threading
import time
from dataclasses import dataclass, field
from pathlib import Path
from typing import Callable
from urllib.parse import urlsplit

from cryptography.hazmat.primitives import serialization
from cryptography.hazmat.primitives.asymmetric import ec
from cryptography.hazmat.primitives.ciphers.aead import AESGCM

from hermes_cli.lina_board_write import Refused, _body
from utils import atomic_json_write, read_json_or_empty

KEY_ENV, SUBJECT_CONFIG = "LINA_PUSH_VAPID_PRIVATE_KEY", "lina.push.subject"
DEFAULT_SUBJECT = "https://github.com/lina-system/lina"
JWT_SECONDS = 12 * 3600  # RFC 8292 allows at most 24 h
TTL_SECONDS = 86400
RECORD_SIZE = 4096
MAX_PLAINTEXT = 3800  # one record: 4096 - 86 header - 16 tag - 1 delimiter, with room to spare
ENDPOINT_LIMIT = 2048
# The push services of Safari (Apple), Chrome and Edge on Android (Google), Firefox (Mozilla) and Edge on Windows.
PUSH_HOSTS = ("fcm.googleapis.com", "android.googleapis.com")
PUSH_HOST_SUFFIXES = (".push.apple.com", ".push.services.mozilla.com", ".notify.windows.com")
log = logging.getLogger(__name__)
_STORE_LOCK = threading.Lock()


def b64u(data: bytes) -> str:
    return base64.urlsafe_b64encode(data).decode().rstrip("=")


def unb64u(text: str) -> bytes:
    """base64url with or without padding -> bytes; ValueError on anything else."""
    text = text.strip()
    if not text or any(c not in "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_=" for c in text):
        raise ValueError("not base64url")
    return base64.urlsafe_b64decode(text + "=" * (-len(text) % 4))


def _public_bytes(key: ec.EllipticCurvePublicKey) -> bytes:
    return key.public_bytes(serialization.Encoding.X962, serialization.PublicFormat.UncompressedPoint)


# ---------------------------------------------------------------- the key
@dataclass(frozen=True)
class Vapid:
    private: ec.EllipticCurvePrivateKey = field(repr=False)
    public: str  # base64url of the uncompressed point: the app's applicationServerKey
    subject: str


def push_subject() -> str:
    """``lina.push.subject`` in config.yaml (a setting, not a secret), default the project URL."""
    from hermes_cli.config import load_config_readonly
    push = ((load_config_readonly().get("lina") or {}).get("push") or {})
    subject = push.get("subject") if isinstance(push, dict) else None
    return (subject.strip() if isinstance(subject, str) else "") or DEFAULT_SUBJECT


def vapid_key() -> tuple[Vapid | None, str]:
    """The key from the environment and the subject from config.yaml, or None and the reason push is off (no key
    text in the reason)."""
    from hermes_cli.config_backend import ConfigBackendUnavailable
    raw = (os.environ.get(KEY_ENV) or "").strip()
    if not raw:
        return None, f"{KEY_ENV} is not set"
    try:
        subject = push_subject()
    except ConfigBackendUnavailable:
        return None, "config.yaml could not be read for lina.push.subject"
    if not subject.startswith(("mailto:", "https://")):
        return None, f"{SUBJECT_CONFIG} must start with mailto: or https://"
    try:
        secret = unb64u(raw)
        if len(secret) != 32:
            raise ValueError("not 32 bytes")
        private = ec.derive_private_key(int.from_bytes(secret, "big"), ec.SECP256R1())
    except ValueError:
        return None, f"{KEY_ENV} is not base64url of a raw P-256 private key"
    return Vapid(private, b64u(_public_bytes(private.public_key())), subject), ""


def key_view() -> dict:
    """``GET /api/lina/push/key``."""
    vapid, _ = vapid_key()
    return {"version": 1, "enabled": vapid is not None, "publicKey": vapid.public if vapid else None}


def origin(endpoint: str) -> str:
    parts = urlsplit(endpoint)
    return f"{parts.scheme}://{parts.netloc}"


def vapid_jwt(vapid: Vapid, endpoint: str, now: float) -> str:
    import jwt
    claims = {"aud": origin(endpoint), "exp": int(now) + JWT_SECONDS, "sub": vapid.subject}
    return jwt.encode(claims, vapid.private, algorithm="ES256", headers={"typ": "JWT"})


# ---------------------------------------------------------------- RFC 8291
def _hmac(key: bytes, data: bytes) -> bytes:
    return hmac.new(key, data, hashlib.sha256).digest()


def encrypt(plaintext: bytes, ua_public: bytes, auth_secret: bytes, *,
            as_private: ec.EllipticCurvePrivateKey | None = None, salt: bytes | None = None) -> bytes:
    """One ``aes128gcm`` message body (RFC 8291 section 3 and RFC 8188, a single record). The sender key and the salt
    are new for every message; they are parameters only so the RFC's example can be reproduced."""
    if len(plaintext) > MAX_PLAINTEXT:
        raise ValueError(f"push payload is {len(plaintext)} bytes, over {MAX_PLAINTEXT}")
    as_private = ec.generate_private_key(ec.SECP256R1()) if as_private is None else as_private
    salt = os.urandom(16) if salt is None else salt
    as_public = _public_bytes(as_private.public_key())
    ua_key = ec.EllipticCurvePublicKey.from_encoded_point(ec.SECP256R1(), ua_public)
    ecdh_secret = as_private.exchange(ec.ECDH(), ua_key)
    prk_key = _hmac(auth_secret, ecdh_secret)
    ikm = _hmac(prk_key, b"WebPush: info\x00" + ua_public + as_public + b"\x01")
    prk = _hmac(salt, ikm)
    cek = _hmac(prk, b"Content-Encoding: aes128gcm\x00\x01")[:16]
    nonce = _hmac(prk, b"Content-Encoding: nonce\x00\x01")[:12]
    header = salt + struct.pack("!IB", RECORD_SIZE, len(as_public)) + as_public
    return header + AESGCM(cek).encrypt(nonce, plaintext + b"\x02", None)


# ---------------------------------------------------------------- subscriptions
def push_dir(root: Path) -> Path:
    return root / "push"


def _subs_dir(root: Path) -> Path:
    return push_dir(root) / "subscriptions"


def sub_id(endpoint: str) -> str:
    return hashlib.sha256(endpoint.encode()).hexdigest()[:32]


def host(endpoint: str) -> str:
    """What the logs name of an endpoint: its host only (the path is the device's capability)."""
    return urlsplit(endpoint).hostname or "?"


def check_endpoint(endpoint: str) -> str:
    endpoint = endpoint.strip()
    if len(endpoint) > ENDPOINT_LIMIT:
        raise Refused(400, f"endpoint is longer than {ENDPOINT_LIMIT} characters")
    try:
        parts = urlsplit(endpoint)
        port = parts.port
    except ValueError as exc:
        raise Refused(400, "endpoint is not a URL") from exc
    name = (parts.hostname or "").lower()
    if parts.scheme != "https" or parts.username or parts.password or port not in (None, 443):
        raise Refused(400, "endpoint must be an https URL with no user or port")
    if not (name in PUSH_HOSTS or name.endswith(PUSH_HOST_SUFFIXES)):
        raise Refused(400, f"endpoint host {name or '?'} is not a known push service")
    return endpoint


def _check_keys(keys: dict) -> dict:
    try:
        p256dh, auth = unb64u(str(keys.get("p256dh") or "")), unb64u(str(keys.get("auth") or ""))
        ec.EllipticCurvePublicKey.from_encoded_point(ec.SECP256R1(), p256dh)
    except ValueError as exc:
        raise Refused(400, "keys.p256dh must be a base64url P-256 public key") from exc
    if len(p256dh) != 65 or len(auth) != 16:
        raise Refused(400, "keys.p256dh is 65 bytes and keys.auth 16, base64url")
    return {"p256dh": b64u(p256dh), "auth": b64u(auth)}


def save_subscription(root: Path, body, now: float | None = None) -> dict:
    """``POST /api/lina/push/subscriptions``: add or refresh this device's subscription (one file per endpoint)."""
    body = _body(body, {"endpoint": str, "expirationTime": int, "keys": dict}, required=("endpoint", "keys"))
    if vapid_key()[0] is None:
        raise Refused(503, "push is not set up on this core")
    endpoint = check_endpoint(body["endpoint"])
    keys = _check_keys(body["keys"])
    now = time.time() if now is None else now
    sid, folder = sub_id(endpoint), _subs_dir(root)
    with _STORE_LOCK:
        path = folder / f"{sid}.json"
        old = read_json_or_empty(path)
        folder.mkdir(parents=True, exist_ok=True)
        for d in (push_dir(root), folder):
            d.chmod(0o700)
        atomic_json_write(path, {"endpoint": endpoint, "keys": keys, "expirationTime": body.get("expirationTime"),
                                 "createdAt": int(old.get("createdAt") or now), "updatedAt": int(now)}, mode=0o600)
    return {"ok": True, "id": sid, "created": not old}


def remove_subscription(root: Path, body) -> dict:
    """``POST /api/lina/push/subscriptions/remove``."""
    body = _body(body, {"endpoint": str}, required=("endpoint",))
    return {"ok": True, "removed": drop(root, sub_id(body["endpoint"].strip()))}


def drop(root: Path, sid: str) -> bool:
    with _STORE_LOCK:
        path = _subs_dir(root) / f"{sid}.json"
        found = path.is_file()
        path.unlink(missing_ok=True)
    return found


def subscriptions(root: Path, now: float) -> dict[str, dict]:
    """The devices to push to; one whose ``expirationTime`` (ms) has passed is removed here."""
    out = {}
    folder = _subs_dir(root)
    for path in sorted(folder.glob("*.json")) if folder.is_dir() else ():
        sub = read_json_or_empty(path)
        if not sub.get("endpoint") or not sub.get("keys"):
            continue
        expires = sub.get("expirationTime")
        if isinstance(expires, (int, float)) and expires <= now * 1000:
            log.info("push: subscription at %s expired, removed", host(sub["endpoint"]))
            drop(root, path.stem)
            continue
        out[path.stem] = sub
    return out


# ---------------------------------------------------------------- one message
Post = Callable[[str, dict, bytes], int]


def http_post(url: str, headers: dict, body: bytes) -> int:
    import httpx
    return httpx.post(url, headers=headers, content=body, timeout=15).status_code


def topic(tag: str) -> str:
    """RFC 8030 ``Topic``: at most 32 base64url characters, so a hash of the tag."""
    return b64u(hashlib.sha256(tag.encode()).digest()[:24])


def send(vapid: Vapid, sub: dict, message: dict, post: Post = http_post, now: float | None = None) -> int:
    """Encrypt *message* for one device and post it -> the push service's status code."""
    now = time.time() if now is None else now
    keys = sub["keys"]
    plaintext = json.dumps(message, ensure_ascii=False, separators=(",", ":")).encode()
    body = encrypt(plaintext, unb64u(keys["p256dh"]), unb64u(keys["auth"]))
    headers = {"Authorization": f"vapid t={vapid_jwt(vapid, sub['endpoint'], now)}, k={vapid.public}",
               "Content-Encoding": "aes128gcm", "Content-Type": "application/octet-stream",
               "TTL": str(TTL_SECONDS), "Urgency": "high", "Topic": topic(message["tag"])}
    return post(sub["endpoint"], headers, body)
