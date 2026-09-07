#!/usr/bin/env python3
"""Architecture smoke for encrypted vault writes and reads."""
import http.cookiejar
import json
import sys
import urllib.error
import urllib.request


root = sys.argv[1].rstrip("/")
base = root + "/api/v1"
password = "new correct battery 55"
marker = "SYNSECRET-architecture-smoke-9f2c"


class Client:
    def __init__(self):
        self.jar = http.cookiejar.CookieJar()
        self.opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(self.jar))
        self.csrf = ""

    def request(self, method, path, data=None, expected=200, idempotency_key=None):
        headers = {
            "Content-Type": "application/json",
            "Origin": root,
            "X-CSRF-Token": self.csrf,
        }
        if idempotency_key:
            headers["Idempotency-Key"] = idempotency_key
        body = None if data is None else json.dumps(data).encode()
        request = urllib.request.Request(base + path, data=body, headers=headers, method=method)
        try:
            response = self.opener.open(request, timeout=15)
        except urllib.error.HTTPError as exc:
            response = exc
        with response:
            assert response.status == expected, f"{method} {path}: {response.status}, expected {expected}"
            assert response.headers.get("Cache-Control") == "no-store", f"cacheable vault response: {path}"
            if response.headers.get("X-CSRF-Token"):
                self.csrf = response.headers["X-CSRF-Token"]
            raw = response.read()
            return json.loads(raw) if raw else None

    def login(self):
        self.csrf = self.request("POST", "/csrf")["csrf_token"]
        result = self.request("POST", "/auth/login", {"username": "admin", "password": password})
        self.csrf = result["csrf_token"]


client = Client()
client.login()
payload = {"name": "architecture smoke", "body": marker}
created = client.request(
    "POST",
    "/items",
    {"item_type": "secure_note", "vault_scope": "personal", "payload": payload, "tags": ["smoke"]},
    expected=201,
    idempotency_key="architecture-smoke-vault-1",
)
item_id = created["id"]
assert created["payload"] == payload, "create response did not round-trip the encrypted payload"

read_back = client.request("GET", f"/items/{item_id}")
assert read_back["payload"] == payload, "read response did not decrypt the stored payload"

updated_payload = {"name": "architecture smoke updated", "body": marker + "-updated"}
updated = client.request(
    "PUT",
    f"/items/{item_id}",
    {"revision": read_back["revision"], "payload": updated_payload},
)
assert updated["revision"] == 2, f"encrypted update revision={updated['revision']}, want 2"
assert client.request("GET", f"/items/{item_id}")["payload"] == updated_payload

client.request("DELETE", f"/items/{item_id}", expected=204)
print("e2e: encrypted vault create/read/update/trash PASS")
