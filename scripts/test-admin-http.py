#!/usr/bin/env python3
"""M2 acceptance over the real API: member lifecycle, idempotent creation,
disable/enable session invalidation, last-admin protection, deletion
cascade confirmation and replay-after-revocation. Synthetic SYNSECRET
markers must never surface in responses; scripts/test-e2e.sh scans the
container logs and database afterwards. Admin password is the state left by
test-auth-http.py."""
import http.cookiejar
import json
import sys
import time
import urllib.error
import urllib.request

root = sys.argv[1]
base = root + "/api/v1"
admin_password = "new correct battery 55"
# Synthetic secrets: if any of these appear in logs, the database or
# responses, the run fails.
initial_password = "SYNSECRET-INITIAL-7a3b worker pw"
member_password = "SYNSECRET-CHANGED-91cd member pw"

failures = []


def check(cond, message):
    if not cond:
        failures.append(message)
        print(f"FAIL: {message}")


class Client:
    def __init__(self):
        self.jar = http.cookiejar.CookieJar()
        self.opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(self.jar))
        self.csrf = ""
        self.user = None

    def raw(self, method, path, data=None, headers=None):
        req_headers = {"Content-Type": "application/json", "Origin": root}
        if headers:
            req_headers.update(headers)
        req = urllib.request.Request(base + path, data=None if data is None else json.dumps(data).encode(),
                                     headers=req_headers, method=method)
        try:
            response = self.opener.open(req, timeout=15)
        except urllib.error.HTTPError as exc:
            response = exc
        with response:
            body = response.read()
            parsed = json.loads(body) if body else None
            return response.status, response.headers, parsed

    def request(self, method, path, data=None, expected=200, idem_key=None, code=None):
        headers = {"X-CSRF-Token": self.csrf}
        if idem_key:
            headers["Idempotency-Key"] = idem_key
        status, hdrs, parsed = self.raw(method, path, data, headers)
        if code is not None:
            check(parsed and parsed.get("code") == code,
                  f"{method} {path}: code={parsed and parsed.get('code')}, want {code}")
        check(status == expected, f"{method} {path}: {status}, expected {expected}")
        if hdrs.get("X-CSRF-Token"):
            self.csrf = hdrs["X-CSRF-Token"]
        return parsed

    def login(self, username, password, expected=200):
        # Denied attempts do not extend the rolling window, so bounded
        # retries after a 429 converge once earlier logins age out.
        for attempt in range(6):
            self.csrf = self.request("POST", "/csrf")["csrf_token"]
            status, _, parsed = self.raw("POST", "/auth/login", {"username": username, "password": password},
                                         {"X-CSRF-Token": self.csrf})
            if status != 429:
                break
            time.sleep(20)
        check(status == expected, f"login {username}: {status}, expected {expected}")
        if expected == 200 and parsed:
            self.csrf = parsed["csrf_token"]
            self.user = parsed["user"]
        return parsed


admin = Client()
admin.login("admin", admin_password)

# ---- 1+7: create a member through the real API, idempotent retries ---------
created = admin.request("POST", "/users",
                        {"username": "worker", "initial_password": initial_password},
                        expected=201, idem_key="e2e-create-worker-0001")
check(created["role"] == "member" and created["status"] == "must_change_password",
      f"created member state wrong: {created}")
replayed = admin.request("POST", "/users",
                         {"username": "worker", "initial_password": initial_password},
                         expected=201, idem_key="e2e-create-worker-0001")
check(replayed["id"] == created["id"], "idempotent replay returned a different member")
admin.request("POST", "/users", {"username": "worker2", "initial_password": initial_password},
              expected=409, idem_key="e2e-create-worker-0001", code="IDEMPOTENCY_KEY_CONFLICT")

# ---- 2: member must change the initial password first ----------------------
worker = Client()
first_login = worker.login("worker", initial_password)
check(first_login["must_change_password"] is True, "first login must require a password change")
worker.request("GET", "/auth/session", expected=200)
worker.request("GET", "/auth/activity", expected=403, code="PASSWORD_CHANGE_REQUIRED")
worker.request("POST", "/auth/password",
               {"current_password": initial_password, "new_password": member_password}, expected=204)
# The change rotated the session: this client now holds the fresh cookie
# (old-session revocation is proven by test-auth-http.py with two clients).
worker.request("GET", "/auth/session", expected=200)
worker.login("worker", member_password)

# ---- 5+6: member reads only own activity; no user or system audit access ---
activity = worker.request("GET", "/auth/activity?limit=100", expected=200)
own_id = worker.user["user_id"]
for item in activity["items"]:
    check(item["actor_id"] == own_id, "activity returned another member's event")
users_view = admin.request("GET", "/users", expected=200)
for item in users_view["items"]:
    check("SYNSECRET" not in json.dumps(item), "member list leaked a synthetic secret")
worker.request("GET", "/users", expected=403)
worker.request("POST", "/users", {"username": "nope", "initial_password": initial_password}, expected=403)
worker.request("GET", "/admin/audit", expected=403)

# ---- 3+4: disable kills sessions immediately; enable does not revive -------
worker_id = None
for item in users_view["items"]:
    if item["username"] == "worker":
        worker_id = item["id"]
check(worker_id is not None, "worker missing from the member list")
disabled = admin.request("POST", f"/users/{worker_id}/disable", expected=200)
check(disabled["status"] == "disabled", f"disable status: {disabled}")
worker.request("GET", "/auth/session", expected=401)
worker.login("worker", member_password, expected=403)
enabled = admin.request("POST", f"/users/{worker_id}/enable", expected=200)
check(enabled["status"] == "active", f"enable status: {enabled}")
worker.request("GET", "/auth/session", expected=401)  # old session stays dead
worker.login("worker", member_password)

# ---- 10: the last administrator cannot be disabled or deleted --------------
admin_id = admin.user["user_id"]
admin.request("POST", f"/users/{admin_id}/disable", expected=409, code="LAST_ADMIN_PROTECTED")
admin.request("DELETE", f"/users/{admin_id}", {"confirm_username": "admin"}, expected=403, code="FORBIDDEN")

# ---- 7: replay after the creating session was revoked is refused -----------
admin2 = Client()
admin2.login("admin", admin_password)
admin2.request("POST", "/users", {"username": "worker2", "initial_password": initial_password},
               expected=201, idem_key="e2e-create-worker2-001")
admin2.request("POST", "/auth/logout", expected=204)
admin2.request("POST", "/users", {"username": "worker2", "initial_password": initial_password},
               expected=401, idem_key="e2e-create-worker2-001")

# ---- 8: deletion confirmation, dead sessions, uniform unknown-user error ---
admin.request("DELETE", f"/users/{worker_id}", {"confirm_username": "WRONG"}, expected=409,
              code="CONFIRMATION_MISMATCH")
admin.request("DELETE", f"/users/{worker_id}", {"confirm_username": "worker"}, expected=204)
worker.request("GET", "/auth/session", expected=401)
worker.login("worker", member_password, expected=401)
deleted_activity = admin.request("GET", "/admin/audit", expected=200)
check(any(item.get("actor_id") == worker_id for item in deleted_activity["items"]),
      "deleted member's audit rows must survive with the opaque id")
# No audit value may repeat the username; target ids are opaque uuids.
check('"worker"' not in json.dumps(deleted_activity["items"]),
      "username snapshot leaked into audit rows")

if failures:
    print(f"e2e: admin acceptance FAILED ({len(failures)} failures)")
    sys.exit(1)
print("e2e: admin member lifecycle/last-admin/deletion/idempotency PASS")
