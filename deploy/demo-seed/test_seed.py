import os
import tempfile
import threading
import time
import unittest
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path

import seed


class StateGuardTests(unittest.TestCase):
    def test_demo_connections_disable_mysql_sandbox_only(self):
        mysql = seed.connection_payload(seed.DATABASES[-1])
        postgres = seed.connection_payload(seed.DATABASES[0])
        self.assertFalse(mysql["sandbox"])
        self.assertTrue(postgres["sandbox"])

    def test_delivery_approval_group_is_customer_operations(self):
        self.assertIn(("Customer Operations", "nadia.pratama", "arif.santoso", "Delivery Orders", "edit"), seed.GROUPS)

    def test_user_lookup_uses_username_field(self):
        self.assertEqual(seed.by_name([{"id": "user-1", "username": "nadia.pratama"}],
                                      "nadia.pratama", "username"), "user-1")

    def test_claims_new_attempt_and_noops_only_after_completion(self):
        with tempfile.TemporaryDirectory() as directory:
            state = Path(directory)
            calls = []

            self.assertTrue(seed.run_once(state, lambda: calls.append("seed")))
            self.assertFalse(seed.run_once(state, lambda: calls.append("rerun")))
            self.assertEqual(calls, ["seed"])

    def test_incomplete_attempt_refuses_with_reset_hint(self):
        with tempfile.TemporaryDirectory() as directory:
            state = Path(directory)
            (state / "attempt").write_text("incomplete\n")
            with self.assertRaisesRegex(seed.SeedError, "reset.*-v"):
                seed.run_once(state, lambda: self.fail("must not mutate"))

    def test_action_failure_leaves_incomplete_attempt(self):
        with tempfile.TemporaryDirectory() as directory:
            state = Path(directory)
            with self.assertRaisesRegex(RuntimeError, "failed"):
                seed.run_once(state, lambda: (_ for _ in ()).throw(RuntimeError("failed")))
            self.assertEqual((state / "attempt").read_text(), "incomplete\n")
            with self.assertRaises(seed.SeedError):
                seed.run_once(state, lambda: self.fail("must not retry mutation"))

    def test_parallel_claim_waits_and_runs_action_once(self):
        with tempfile.TemporaryDirectory() as directory:
            state = Path(directory)
            entered = threading.Event()
            release = threading.Event()
            second_started = threading.Event()
            calls = []

            def first_action():
                calls.append("seed")
                entered.set()
                self.assertTrue(release.wait(2))

            def second():
                second_started.set()
                return seed.run_once(state, lambda: calls.append("parallel seed"))

            with ThreadPoolExecutor(max_workers=2) as pool:
                first = pool.submit(seed.run_once, state, first_action)
                self.assertTrue(entered.wait(2))
                other = pool.submit(second)
                self.assertTrue(second_started.wait(2))
                time.sleep(0.05)
                self.assertFalse(other.done())
                release.set()
                self.assertTrue(first.result(timeout=2))
                self.assertFalse(other.result(timeout=2))
            self.assertEqual(calls, ["seed"])


@unittest.skipUnless(os.environ.get("SQLWISE_VERIFY_DEMO") == "1", "requires the running demo Compose stack")
class LiveDemoVerification(unittest.TestCase):
    def login(self, username):
        client = seed.API()
        client.login(username, os.environ["SQLWISE_DEMO_PASSWORD"])
        return client

    def databases(self, client):
        return {db["name"]: db for db in client.request("GET", "/auth/session")["databases"]}

    def detail(self, client, database, title):
        prefix = f"/databases/{database['id']}"
        history = client.request("GET", prefix + "/migrations")
        draft = next(draft for draft in history["drafts"] if draft["title"] == title)
        return prefix, history, client.request("GET", f"{prefix}/migrations/{draft['id']}")

    def test_seeded_access_review_and_worker_outcomes(self):
        nadia = self.login("nadia.pratama")
        self.assertEqual(set(self.databases(nadia)), {"Delivery Orders"})

        maya = self.login("maya.putri")
        maya_dbs = self.databases(maya)
        self.assertEqual({name: row["permission"] for name, row in maya_dbs.items()},
                         {"Billing Ledger": "edit", "Stock Catalog": "view"})

        bima_dbs = self.databases(self.login("bima.hakim"))
        self.assertEqual({name: row["permission"] for name, row in bima_dbs.items()},
                         {"Stock Catalog": "edit"})
        self.assertEqual({name: row["permission"] for name, row in self.databases(self.login("ratih.wulandari")).items()},
                         {"Stock Catalog": "view"})
        self.assertEqual(self.databases(self.login("farah.ramadhan")), {})

        prefix, _, delivery = self.detail(nadia, self.databases(nadia)["Delivery Orders"],
                                          "Add delivery instructions")
        [delivery_request] = delivery["requests"]
        self.assertEqual(delivery_request["status"], "pending_review")
        self.assertEqual([approval["group_name"] for approval in delivery_request["approvals"]],
                         ["Customer Operations"])
        self.assertEqual(nadia.request("GET", prefix + "/jobs")["jobs"], [])

        prefix, billing, _ = self.detail(maya, maya_dbs["Billing Ledger"], "Record payment reference")
        self.assertEqual(billing["current_version"], 2)
        self.assertEqual(len(billing["committed"]), 2)
        self.assertEqual(len(billing["head"]["active"]), 2)
        self.assertEqual(billing["head"]["pending"], [])
        jobs = maya.request("GET", prefix + "/jobs")["jobs"]
        self.assertEqual(len(jobs), 2)
        self.assertTrue(all(job["status"] == "succeeded" and job["effect"] == "committed" for job in jobs))

        for username, database_name, titles in (
            ("reza.nugraha", "Dispatch Schedule", ("Add departure window", "Add carrier notes")),
            ("ilham.wijaya", "Shipment Archive", ("Backfill shipment service level",)),
        ):
            client = self.login(username)
            database = self.databases(client)[database_name]
            for title in titles:
                prefix, history, detail = self.detail(client, database, title)
                [request] = detail["requests"]
                self.assertEqual(request["status"], "approved", title)
                self.assertEqual(request["expected_version"], 0, title)
                self.assertEqual(history["current_version"], 0, title)
                self.assertEqual(client.request("GET", prefix + "/jobs")["jobs"], [], title)

if __name__ == "__main__":
    unittest.main()
