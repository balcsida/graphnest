"""Offline checks for the pinned CodeGraph reference, with no Go dependencies."""
import hashlib
import json
from pathlib import Path
import sqlite3
import unittest

ROOT = Path(__file__).resolve().parents[1] / "fixtures" / "codegraph"
NODE_KINDS = set("file module class struct interface trait protocol function method property field variable constant enum enum_member type_alias namespace parameter import export route component union".split())
EDGE_KINDS = set("contains calls imports exports extends implements references type_of returns instantiates overrides decorates navigates".split())


class ReferenceTest(unittest.TestCase):
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
        self.assertTrue(any(row["node"]["name"] == "greet" for row in library["lib-getCallers"]))
        self.assertIn("core.ts", library["lib-getFileDependencies"])


if __name__ == "__main__":
    unittest.main()
