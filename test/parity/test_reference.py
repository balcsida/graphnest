"""Offline checks for the pinned CodeGraph reference, with no Go dependencies."""
import hashlib
import json
from pathlib import Path
import sqlite3
import statistics
import unittest

ROOT = Path(__file__).resolve().parents[1] / "fixtures" / "codegraph"
NODE_KINDS = set("file module class struct interface trait protocol function method property field variable constant enum enum_member type_alias namespace parameter import export route component union".split())
EDGE_KINDS = set("contains calls imports exports extends implements references type_of returns instantiates overrides decorates navigates".split())


class ReferenceTest(unittest.TestCase):
    def test_workflow_timings(self):
        path = ROOT / "workflow-baseline.json"
        self.assertTrue(path.is_file(), "capture the five-run workflow baseline")
        baseline = json.loads(path.read_text())
        manifest = json.loads((ROOT / "manifest.json").read_text())
        self.assertEqual(baseline["producer"], manifest["producer"])
        self.assertEqual(baseline["source_sha256"], {name: digest for name, digest in manifest["sha256"].items() if name.startswith("source/")})
        for name, digest in baseline["harness_sha256"].items():
            self.assertEqual(hashlib.sha256((ROOT.parents[2] / name).read_bytes()).hexdigest(), digest, name)
        self.assertEqual(baseline["reference_db_sha256"], manifest["sha256"]["reference.db"])
        self.assertEqual(baseline["reference_answers_sha256"], manifest["sha256"]["library-expected.json"])
        self.assertEqual(baseline["configuration"], json.loads((ROOT / "source/codegraph.json").read_text()))
        self.assertEqual(baseline["database_bytes"], (ROOT / "reference.db").stat().st_size)
        self.assertEqual(baseline["schema_version"], 9)
        self.assertEqual(baseline["method"]["runs"], 5)
        self.assertEqual(baseline["method"]["warmups_per_run"], 5)
        self.assertEqual(baseline["method"]["samples_per_run"], 100)
        workflows = baseline["workflow_timings"]
        self.assertEqual(set(workflows), {"lib-getCallers", "mcp-explore-source", "ui-flow-branch"})
        for task, workflow in workflows.items():
            self.assertEqual(len(workflow["runs"]), 5, task)
            for run in workflow["runs"]:
                samples = sorted(run["samples_ms"])
                self.assertEqual(len(samples), 100, task)
                self.assertTrue(all(0 < value <= 5000 for value in samples), task)
                self.assertEqual(run["p50_ms"], samples[49], task)
                self.assertEqual(run["p95_ms"], samples[94], task)
                self.assertGreater(run["response_bytes"]["min"], 2, task)
                self.assertGreaterEqual(run["response_bytes"]["max"], run["response_bytes"]["min"], task)
                self.assertGreater(run["process_max_rss_bytes"], 0, task)
                self.assertGreater(run["process_rss_bytes"], 0, task)
            self.assertEqual(workflow["median_p50_ms"], statistics.median(run["p50_ms"] for run in workflow["runs"]), task)
            self.assertEqual(workflow["median_p95_ms"], statistics.median(run["p95_ms"] for run in workflow["runs"]), task)

    def test_reference_contract(self):
        self.assertTrue((ROOT / "manifest.json").is_file(), "generate the pinned reference first")
        manifest = json.loads((ROOT / "manifest.json").read_text())
        self.assertEqual(manifest["producer"]["commit"], "b9ca4b7981116909900368cc1686a1074cd4d4c1")
        self.assertEqual(manifest["producer"]["environment"], {"CODEGRAPH_KERNEL": "0", "CODEGRAPH_NO_RELAUNCH": "1", "DO_NOT_TRACK": "1", "LANG": "C.UTF-8", "LC_ALL": "C.UTF-8", "TZ": "UTC"})
        self.assertEqual(manifest["producer"]["home"], "fresh empty temporary directory")
        for name, digest in manifest["sha256"].items():
            self.assertEqual(hashlib.sha256((ROOT / name).read_bytes()).hexdigest(), digest, name)
        self.assertEqual({name for name in manifest["sha256"] if name.startswith("source/")}, {str(path.relative_to(ROOT)) for path in (ROOT / "source").rglob("*") if path.is_file()})
        expected = json.loads((ROOT / "expected.json").read_text())
        with sqlite3.connect(f"file:{ROOT / 'reference.db'}?mode=ro", uri=True) as db:
            self.assertEqual(db.execute("PRAGMA integrity_check").fetchall(), [("ok",)])
            self.assertEqual(db.execute("PRAGMA foreign_key_check").fetchall(), [])
            for case in expected:
                self.assertEqual([list(row) for row in db.execute(case["sql"])], case["rows"], case["id"])
            calls = db.execute("SELECT s.name,t.name FROM edges e JOIN nodes s ON s.id=e.source JOIN nodes t ON t.id=e.target WHERE e.kind='calls'").fetchall()
            self.assertIn(("greet", "normalize"), calls)
            self.assertGreater(db.execute("SELECT COUNT(*) FROM files").fetchone()[0], 0)
            self.assertEqual(db.execute("SELECT name FROM nodes WHERE name='mustNotBeIndexed'").fetchall(), [])
            self.assertEqual(db.execute("SELECT MAX(version) FROM schema_versions").fetchone()[0], 9)
            indexed = {row[0] for row in db.execute("SELECT path FROM files")}
            self.assertEqual(indexed, {str(path.relative_to(ROOT / "source")) for path in (ROOT / "source").rglob("*") if path.is_file() and path.suffix not in (".json",) and path.name != "excluded.ts"})
            self.assertTrue(db.execute("SELECT id FROM nodes WHERE name='unicodeCaller'").fetchone())
        contract = json.loads((ROOT / "synthetic-contract.json").read_text())
        self.assertEqual(set(contract["node_kinds"]), NODE_KINDS)
        self.assertEqual(set(contract["edge_kinds"]), EDGE_KINDS)
        self.assertEqual(contract["origin"], "synthetic schema vocabulary; not producer extraction")
        self.assertEqual({node["kind"] for node in contract["nodes"]}, set(contract["node_kinds"]))
        self.assertEqual({edge["kind"] for edge in contract["edges"]}, set(contract["edge_kinds"]))
        ids = {node["id"] for node in contract["nodes"]}
        self.assertTrue(all(edge["source"] in ids and edge["target"] in ids for edge in contract["edges"]))
        self.assertTrue((ROOT / "library-expected.json").is_file(), "capture real producer library answers")
        library = json.loads((ROOT / "library-expected.json").read_text())
        self.assertEqual(manifest["reference_tasks"], sorted(library), "manifest reference tasks differ from library answers")
        self.assertTrue(any(row["node"]["name"] == "greet" for row in library["lib-getCallers"]))
        self.assertIn("core.ts", library["lib-getFileDependencies"])
        self.assertTrue("ui-flow-branch" in library, "capture committed-snapshot workflow answers")
        self.assertTrue(any(hop.get("edge", {}).get("when") == "enabled" for flow in library["ui-flow-branch"]["flows"] for hop in flow["hops"] if hop.get("edge")))
        self.assertEqual(library["ui-flow-missing"]["flows"], [])
        self.assertEqual(library["ui-flow-invalid"]["code"], "bad-request")
        self.assertTrue(any(link["when"] == "enabled" for link in library["ui-screens-navigation"]["links"]))
        self.assertTrue(any(step["kind"] == "fork" and step["on"] == "enabled" for step in library["ui-steps-branch"]["program"]["root"]))
        self.assertTrue(any(link["when"] == "enabled" for link in library["ui-steps-screen"]["links"]))
        self.assertEqual(library["cli-affected-transitive"]["affectedTests"], ["consumer.test.ts"])
        self.assertEqual(library["cli-affected-unrelated"]["affectedTests"], [])
        self.assertEqual(library["ui-source-verbatim"]["lines"], (ROOT / "source/consumer.ts").read_text().splitlines()[1:7])
        self.assertTrue(library["ui-source-drift"]["drift"])
        self.assertNotIn("lines", library["ui-source-drift"])
        self.assertEqual({node["name"] for node in library["ui-node-types"]["hierarchy"]["ancestors"]["items"]}, {"Base", "Greeter"})
        self.assertEqual([node["name"] for node in library["ui-deadcode"]["rows"]["items"]], ["dormantUtility"])
        self.assertEqual(library["ui-trails-readonly"]["code"], "refused")
        self.assertTrue(library["ui-trails-reload"]["trails"][0]["intact"])
        self.assertEqual([hop["node"]["name"] for hop in library["ui-trails-open-flow"]["flows"][0]["hops"]], ["run", "normalize"])
        self.assertEqual(library["ui-trails-delete"]["trails"], [])


if __name__ == "__main__":
    unittest.main()
