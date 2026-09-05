#!/usr/bin/env python3
"""T06 container smoke with synthetic credentials from test-e2e.sh only."""
import http.cookiejar
import json
import sys
import urllib.error
import urllib.request

base = sys.argv[1] + "/api/v1"
old_password = "correct horse battery 42"
new_password = "new correct battery 55"


class Client:
    def __init__(self):
        self.jar = http.cookiejar.CookieJar()
        self.opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(self.jar))
        self.csrf = ""

    def request(self, method, path, data=None, expected=200):
        headers = {"Content-Type": "application/json", "Origin": sys.argv[1], "X-CSRF-Token": self.csrf}
        req = urllib.request.Request(base + path, data=None if data is None else json.dumps(data).encode(), headers=headers, method=method)
        try:
            response = self.opener.open(req, timeout=15)
        except urllib.error.HTTPError as exc:
            response = exc
        with response:
            assert response.status == expected, f"{method} {path}: {response.status}, expected {expected}"
            assert response.headers.get("Cache-Control") == "no-store", f"cacheable auth response: {path}"
            if response.headers.get("X-CSRF-Token"):
                self.csrf = response.headers["X-CSRF-Token"]
            body = response.read()
            return json.loads(body) if body else None

    def login(self, password, expected=200):
        self.csrf = self.request("POST", "/csrf")["csrf_token"]
        result = self.request("POST", "/auth/login", {"username": "admin", "password": password}, expected)
        if expected == 200:
            self.csrf = result["csrf_token"]
        return result


first, second = Client(), Client()
first.login(old_password)
second.login(old_password)
assert not first.request("GET", "/auth/session")["user"]["must_change_password"]
assert len(first.request("GET", "/auth/sessions")["items"]) == 2
first.request("POST", "/auth/password", {"current_password": old_password, "new_password": new_password}, 204)
second.request("GET", "/auth/session", expected=401)
assert len(first.request("GET", "/auth/sessions")["items"]) == 1
first.request("POST", "/auth/session/activity", expected=204)
first.request("POST", "/auth/logout", expected=204)
first.request("GET", "/auth/session", expected=401)
first.login(old_password, expected=401)
first.login(new_password)
first.request("POST", "/auth/logout", expected=204)
print("e2e: auth login/session/password rotation/revocation/logout PASS")
