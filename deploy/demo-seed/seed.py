#!/usr/bin/env python3
"""Seed the disposable SQLwise demo through its public API."""
import fcntl
import http.cookiejar
import json
import os
from pathlib import Path
import re
import sys
import time
import urllib.error
import urllib.parse
import urllib.request


BASE = "http://app:8080/api/v1"
STATE = Path("/state")
DATABASES = (
    {"name": "Delivery Orders", "dbms": "postgresql", "database": "delivery_orders"},
    {"name": "Billing Ledger", "dbms": "postgresql", "database": "billing_ledger"},
    {"name": "Dispatch Schedule", "dbms": "postgresql", "database": "dispatch_schedule"},
    {"name": "Shipment Archive", "dbms": "postgresql", "database": "shipment_archive"},
    {"name": "Stock Catalog", "dbms": "mysql", "database": "stock_catalog"},
)
PEOPLE = {
    "nadia.pratama": "Nadia demo editor",
    "arif.santoso": "Arif demo leader",
    "ratih.wulandari": "Ratih catalog viewer",
    "farah.ramadhan": "Account with no grants",
    "ayu.puspita": "Additional catalog viewer",
    "maya.putri": "Finance editor",
    "dimas.saputra": "Finance leader",
    "bima.hakim": "Warehouse Operations editor",
    "sari.lestari": "Warehouse Operations leader",
    "reza.nugraha": "Fleet Operations editor",
    "dewi.kusuma": "Fleet Operations leader",
    "ilham.wijaya": "Data Operations editor",
    "rani.maharani": "Data Operations leader",
}
GROUPS = (
    ("Finance", "maya.putri", "dimas.saputra", "Billing Ledger", "edit"),
    ("Customer Operations", "nadia.pratama", "arif.santoso", "Delivery Orders", "edit"),
    ("Warehouse Operations", "bima.hakim", "sari.lestari", "Stock Catalog", "edit"),
    ("Fleet Operations", "reza.nugraha", "dewi.kusuma", "Dispatch Schedule", "edit"),
    ("Data Operations", "ilham.wijaya", "rani.maharani", "Shipment Archive", "edit"),
    ("Catalog Readers", "ratih.wulandari", None, "Stock Catalog", "view"),
)


class SeedError(RuntimeError):
    pass


def config_from_env():
    required = ("SQLWISE_ADMIN_USERNAME", "SQLWISE_ADMIN_PASSWORD", "SQLWISE_DEMO_PASSWORD")
    missing = [name for name in required if not os.environ.get(name)]
    if missing:
        raise SeedError("Missing required environment: " + ", ".join(missing))
    values = {name: os.environ[name] for name in required}
    if not re.fullmatch(r"[a-z0-9][a-z0-9_.-]{2,63}", values["SQLWISE_ADMIN_USERNAME"].strip().lower()):
        raise SeedError("SQLWISE_ADMIN_USERNAME is invalid")
    for name in ("SQLWISE_ADMIN_PASSWORD", "SQLWISE_DEMO_PASSWORD"):
        if len(values[name]) < 12 or len(values[name].encode("utf-8", errors="surrogatepass")) > 128:
            raise SeedError(f"{name} must be at least 12 characters and at most 128 UTF-8 bytes")
    return values


def run_once(state_dir, action):
    """Serialize seeds; a failed claimed run stays incomplete for safe recovery."""
    state_dir = Path(state_dir)
    state_dir.mkdir(parents=True, exist_ok=True)
    with (state_dir / "seed.lock").open("a") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        attempt = state_dir / "attempt"
        if attempt.exists():
            if attempt.read_text() == "complete\n":
                return False
            raise SeedError("A previous seed attempt is incomplete; use the README's scoped demo reset (docker compose down -v)")
        fd = os.open(attempt, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(fd, "w") as marker:
            marker.write("incomplete\n")
            marker.flush()
            os.fsync(marker.fileno())
        action()
        temp = state_dir / "attempt.tmp"
        with temp.open("w") as marker:
            marker.write("complete\n")
            marker.flush()
            os.fsync(marker.fileno())
        os.replace(temp, attempt)
        return True


class API:
    def __init__(self):
        self.jar = http.cookiejar.CookieJar()
        self.opener = urllib.request.build_opener(
            urllib.request.ProxyHandler({}), urllib.request.HTTPCookieProcessor(self.jar)
        )
        self.csrf = ""

    def request(self, method, path, body=None):
        headers = {"Accept": "application/json"}
        data = None
        if body is not None:
            data = json.dumps(body).encode()
            headers["Content-Type"] = "application/json"
        if method != "GET":
            headers["X-CSRF-Token"] = self.csrf
        request = urllib.request.Request(BASE + path, data=data, headers=headers, method=method)
        try:
            with self.opener.open(request, timeout=30) as response:
                status = response.status
                raw = response.read(4 << 20)
        except urllib.error.HTTPError as error:
            raise SeedError(f"{method} {path} failed with HTTP {error.code}") from None
        except (urllib.error.URLError, TimeoutError, OSError):
            raise SeedError(f"{method} {path} could not reach the SQLwise API") from None
        if status >= 300:
            raise SeedError(f"{method} {path} failed with HTTP {status}")
        try:
            result = json.loads(raw) if raw else {}
        except (UnicodeDecodeError, json.JSONDecodeError):
            raise SeedError(f"{method} {path} returned invalid JSON") from None
        if "error" in result:
            raise SeedError(f"{method} {path} returned an API error")
        return result.get("data", {})

    def login(self, username, password):
        challenge = self.request("GET", "/auth/login-challenge")
        self.csrf = challenge["csrf_token"]
        session = self.request("POST", "/auth/login", {"username": username, "password": password})
        self.csrf = session["csrf_token"]


def by_name(rows, name, field="name"):
    for row in rows:
        if row[field] == name:
            return row["id"]
    raise SeedError("The SQLwise API response omitted a requested record")


def connection_payload(item):
    mysql = item["dbms"] == "mysql"
    return {
        "name": item["name"], "dbms": item["dbms"],
        "host": "mysql" if mysql else "target", "port": "3306" if mysql else "5432",
        "database": item["database"], "username": "sqlwise_target" if mysql else "target_owner",
        "password": "sqlwise_target_dev_only" if mysql else "target_owner_dev_only",
        "tls": "disable", "sandbox": not mysql,
    }


def seed_demo(config):
    admin = API()
    admin.login(config["SQLWISE_ADMIN_USERNAME"], config["SQLWISE_ADMIN_PASSWORD"])

    for username in PEOPLE:
        admin.request("POST", "/admin/users", {"username": username, "password": config["SQLWISE_DEMO_PASSWORD"]})
    users = admin.request("GET", "/admin/users")["users"]
    user_ids = {name: by_name(users, name, "username") for name in PEOPLE}

    groups = {}
    for group, editor, leader, _database, _permission in GROUPS:
        admin.request("POST", "/admin/groups", {"name": group})
    snapshot = admin.request("GET", "/admin/groups")
    group_ids = {name: by_name(snapshot["groups"], name) for name, *_ in GROUPS}
    for name, editor, leader, database_name, permission in GROUPS:
        group_id = group_ids[name]
        groups[name] = {"id": group_id, "editor": editor, "leader": leader}
        members = [editor] + ([leader] if leader else [])
        if name == "Catalog Readers":
            members.append("ayu.puspita")
        for member in members:
            admin.request("POST", f"/admin/groups/{group_id}/members", {"user_id": user_ids[member]})
        if leader:
            admin.request("POST", f"/admin/groups/{group_id}/leader", {"user_id": user_ids[leader]})

    db_ids = {}
    for item in DATABASES:
        result = admin.request("POST", "/admin/databases", connection_payload(item))
        db_ids[item["name"]] = result["form"]["id"]
    for group, _editor, _leader, database_name, permission in GROUPS:
        admin.request("POST", f"/admin/groups/{groups[group]['id']}/databases",
                      {"database_id": db_ids[database_name], "permission": permission})
    admin.request("POST", f"/admin/groups/{groups['Finance']['id']}/databases",
                  {"database_id": db_ids["Stock Catalog"], "permission": "view"})

    demo = API()
    demo.login("nadia.pratama", config["SQLWISE_DEMO_PASSWORD"])
    create_request(demo, "Delivery Orders", db_ids, "Add delivery instructions",
                   "ALTER TABLE orders ADD COLUMN delivery_instructions text;",
                   "ALTER TABLE orders DROP COLUMN delivery_instructions;",
                   [], execute=False, expected_status="pending_review")

    finance = API()
    finance.login("maya.putri", config["SQLWISE_DEMO_PASSWORD"])
    finance_lead = API()
    finance_lead.login("dimas.saputra", config["SQLWISE_DEMO_PASSWORD"])
    create_request(finance, "Billing Ledger", db_ids, "Record invoice issuer",
                   "ALTER TABLE invoices ADD COLUMN issued_by text;", "ALTER TABLE invoices DROP COLUMN issued_by;",
                   [(finance_lead, groups["Finance"]["id"])], execute=True)
    create_request(finance, "Billing Ledger", db_ids, "Record payment reference",
                   "ALTER TABLE invoices ADD COLUMN payment_reference text;", "ALTER TABLE invoices DROP COLUMN payment_reference;",
                   [(finance_lead, groups["Finance"]["id"])], execute=True)

    fleet = API()
    fleet.login("reza.nugraha", config["SQLWISE_DEMO_PASSWORD"])
    fleet_lead = API()
    fleet_lead.login("dewi.kusuma", config["SQLWISE_DEMO_PASSWORD"])
    create_request(fleet, "Dispatch Schedule", db_ids, "Add departure window",
                   "ALTER TABLE routes ADD COLUMN departure_window text;", "ALTER TABLE routes DROP COLUMN departure_window;",
                   [(fleet_lead, groups["Fleet Operations"]["id"])], execute=False)
    create_request(fleet, "Dispatch Schedule", db_ids, "Add carrier notes",
                   "ALTER TABLE routes ADD COLUMN carrier_notes text;", "ALTER TABLE routes DROP COLUMN carrier_notes;",
                   [(fleet_lead, groups["Fleet Operations"]["id"])], execute=False)

    data_editor = API()
    data_editor.login("ilham.wijaya", config["SQLWISE_DEMO_PASSWORD"])
    data_lead = API()
    data_lead.login("rani.maharani", config["SQLWISE_DEMO_PASSWORD"])
    create_request(data_editor, "Shipment Archive", db_ids, "Backfill shipment service level",
                   "", "UPDATE shipments SET service_level = NULL WHERE id BETWEEN 1 AND 1000 AND service_level = 'standard';",
                   [(data_lead, groups["Data Operations"]["id"])], execute=False,
                   batch={"schema": "public", "table": "shipments", "key": "id",
                          "column": "service_level", "value": "standard", "batch_size": 100})


def create_request(editor, database_name, db_ids, title, sql, sql_down, leaders, execute, batch=None,
                   expected_status="approved"):
    database_id = db_ids[database_name]
    prefix = f"/databases/{urllib.parse.quote(database_id, safe='')}"
    history = editor.request("GET", prefix + "/migrations")
    body = {"title": title, "sql": sql, "sql_down": sql_down}
    if batch:
        body["batch"] = batch
    draft = editor.request("POST", prefix + "/migrations", body)
    request = editor.request("POST", f"{prefix}/migrations/{draft['id']}/requests", {
        "operation": "up", "expected_revision": draft["revision"],
        "expected_definition_hash": draft["definition_hash"], "expected_version": history["current_version"],
    })
    request = editor.request("POST", f"{prefix}/requests/{request['id']}/submit", {})
    for leader, group_id in leaders:
        leader.request("POST", f"{prefix}/requests/{request['id']}/approve", {"group_id": group_id})
    request = editor.request("GET", f"{prefix}/requests/{request['id']}")
    if request["status"] != expected_status:
        raise SeedError(f"Request for {title!r} did not reach {expected_status} status")
    if execute:
        job = editor.request("POST", f"{prefix}/migrations/{draft['id']}/execute", {
            "request_id": request["id"], "idempotency_key": "demo-seed-" + draft["id"],
        })
        poll_job(editor, prefix, job["job_id"])


def poll_job(api, prefix, job_id):
    for _ in range(180):
        job = api.request("GET", f"{prefix}/jobs/{urllib.parse.quote(job_id, safe='')}")
        if job["terminal"]:
            if job["status"] != "succeeded" or job["effect"] != "committed":
                raise SeedError("A requested migration job did not complete successfully")
            return
        time.sleep(1)
    raise SeedError("A migration job did not reach a terminal state before timeout")


def main():
    config = config_from_env()
    if run_once(STATE, lambda: seed_demo(config)):
        print("SQLwise demo seed completed.")
    else:
        print("SQLwise demo seed already completed; no changes made.")


if __name__ == "__main__":
    try:
        main()
    except SeedError as error:
        print(f"SQLwise demo seed failed: {error}", file=sys.stderr)
        sys.exit(1)
    except Exception:
        print("SQLwise demo seed failed due to an unexpected error; state remains incomplete.", file=sys.stderr)
        sys.exit(1)
