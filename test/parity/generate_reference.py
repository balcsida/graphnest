#!/usr/bin/env python3
"""Build a fresh pinned CodeGraph producer and regenerate its reference fixture."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import sqlite3
import subprocess
import tarfile
import tempfile
import time

COMMIT = "b9ca4b7981116909900368cc1686a1074cd4d4c1"
NODE = "v24.13.0"
PRODUCER_ENV = {"CODEGRAPH_KERNEL": "0", "CODEGRAPH_NO_RELAUNCH": "1", "DO_NOT_TRACK": "1", "LANG": "C.UTF-8", "LC_ALL": "C.UTF-8", "TZ": "UTC"}
BUILD = "fresh git archive of pinned commit; npm ci --ignore-scripts --no-audit --no-fund; tsc; npm run copy-assets"
FIXTURE = Path(__file__).resolve().parents[1] / "fixtures" / "codegraph"
QUERIES = {
    "node-kinds": "SELECT kind,COUNT(*) FROM nodes GROUP BY kind ORDER BY kind",
    "relation-kinds": "SELECT kind,COUNT(*) FROM edges GROUP BY kind ORDER BY kind",
    "ts-call-normalize": "SELECT s.name,t.name,s.file_path,t.file_path FROM edges e JOIN nodes s ON s.id=e.source JOIN nodes t ON t.id=e.target WHERE e.kind='calls' ORDER BY 1,2,3,4",
    "inheritance": "SELECT e.kind,s.name,t.name FROM edges e JOIN nodes s ON s.id=e.source JOIN nodes t ON t.id=e.target WHERE e.kind IN ('extends','implements','overrides') ORDER BY 1,2,3",
    "files": "SELECT path,language,content_hash,node_count,generated FROM files ORDER BY path",
    "unresolved": "SELECT reference_name,reference_kind,file_path,status FROM unresolved_refs ORDER BY 1,2,3,4",
    "excluded-file": "SELECT name FROM nodes WHERE name='mustNotBeIndexed'",
    "schema-version": "SELECT version,description FROM schema_versions ORDER BY version",
    "fts-normalize": "SELECT id,name,qualified_name FROM nodes_fts WHERE nodes_fts MATCH 'normalize' ORDER BY id",
}


def write_json(path, value):
    path.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n")


def logical_snapshot(db):
    tables = [row[0] for row in db.execute("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'nodes_fts%' AND name NOT LIKE 'sqlite_%' ORDER BY name")]
    return {table: sorted([list(row) for row in db.execute(f'SELECT * FROM "{table}"')], key=lambda row: json.dumps(row, sort_keys=True)) for table in tables}


def produce(upstream, node, destination):
    with tempfile.TemporaryDirectory(prefix="graphnest-codegraph-reference-") as temporary:
        root = Path(temporary) / "source"
        shutil.copytree(FIXTURE / "source", root)
        for path in root.rglob("*"):
            os.utime(path, (0, 0))
        # Extraction must not read user config or inherit fact-changing feature flags.
        home = Path(temporary) / "home"
        home.mkdir()
        env = {**PRODUCER_ENV, "HOME": str(home), "TMPDIR": temporary, "PATH": str(Path(node).parent) + ":/usr/bin:/bin:/usr/sbin:/sbin"}
        subprocess.run([node, "--no-warnings", str(Path(__file__).with_name("reference.mjs")), str(upstream), str(root), str(Path(temporary) / "metrics.json")], env=env, check=True)
        databases = list(root.rglob("*.db"))
        if len(databases) != 1:
            raise RuntimeError(f"expected one producer database, found {databases}")
        destination.unlink(missing_ok=True)
        # backup() incorporates WAL before sanitizing a separate copy.
        with sqlite3.connect(databases[0]) as original, sqlite3.connect(destination) as db:
            original.backup(db)
            tables = [row[0] for row in db.execute("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'nodes_fts%' AND name NOT LIKE 'sqlite_%' ORDER BY name")]
            for table in tables:
                for column in db.execute(f'PRAGMA table_info("{table}")').fetchall():
                    name, kind = column[1:3]
                    if name in ("updated_at", "applied_at", "modified_at", "indexed_at"):
                        db.execute(f'UPDATE "{table}" SET "{name}"=0')
                    elif kind == "TEXT":
                        db.execute(f'UPDATE "{table}" SET "{name}"=replace("{name}",?,?) WHERE instr("{name}",?)>0', (str(root), "/fixture", str(root)))
            db.commit()
            db.execute("PRAGMA wal_checkpoint(TRUNCATE)")
            db.execute("PRAGMA journal_mode=DELETE")
            db.execute("VACUUM")
            expected = [{"id": key, "sql": sql, "rows": [list(row) for row in db.execute(sql)]} for key, sql in QUERIES.items()]
            snapshot = logical_snapshot(db)
            schema = "\n".join(row[0] for row in db.execute("SELECT sql FROM sqlite_master WHERE sql IS NOT NULL ORDER BY type,name")) + "\n"
            timings = []
            for _ in range(105):
                start = time.perf_counter_ns()
                db.execute(QUERIES["ts-call-normalize"]).fetchall()
                timings.append((time.perf_counter_ns() - start) / 1e6)
        metrics = json.loads((Path(temporary) / "metrics.json").read_text())
        metrics["sqlite_warm_call_query_ms"] = {"samples": 100, "p50": sorted(timings[5:])[49], "p95": sorted(timings[5:])[94]}
        library = json.loads((Path(temporary) / "metrics.json.answers").read_text())
        return expected, snapshot, schema, metrics, library


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--upstream", type=Path, required=True)
    parser.add_argument("--node", default="node")
    parser.add_argument("--check", action="store_true", help="regenerate twice and compare to committed answers without writes")
    args = parser.parse_args()
    upstream = args.upstream.resolve()
    actual = subprocess.check_output(["git", "-C", str(upstream), "rev-parse", "HEAD"], text=True).strip()
    if actual != COMMIT:
        parser.error(f"producer must be {COMMIT}, got {actual}")
    if subprocess.check_output(["git", "-C", str(upstream), "diff", "HEAD", "--"], text=True):
        parser.error("producer has tracked modifications")
    args.node = str(Path(shutil.which(args.node)).resolve())
    runtime = subprocess.check_output([args.node, "--version"], text=True, env={}).strip()
    if runtime != NODE:
        parser.error(f"use pinned Node {NODE}, got {runtime}")
    with tempfile.TemporaryDirectory(prefix="graphnest-reference-output-") as temporary:
        # Ignore checkout dist/node_modules entirely; execute only a fresh pinned build.
        archive = Path(temporary) / "producer.tar"
        subprocess.run(["git", "-C", str(upstream), "archive", "--format=tar", "--prefix=producer/", "--output", str(archive), COMMIT], check=True)
        with tarfile.open(archive) as source:
            source.extractall(temporary, filter="data")
        upstream = Path(temporary) / "producer"
        build_env = {key: os.environ[key] for key in ("HOME", "TMPDIR", "HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "SSL_CERT_FILE", "NODE_EXTRA_CA_CERTS") if key in os.environ}
        build_env.update(PATH=str(Path(args.node).parent) + ":/usr/bin:/bin:/usr/sbin:/sbin", DO_NOT_TRACK="1")
        subprocess.run(["npm", "ci", "--ignore-scripts", "--no-audit", "--no-fund"], cwd=upstream, env=build_env, check=True)
        subprocess.run([args.node, "node_modules/typescript/bin/tsc"], cwd=upstream, env=build_env, check=True)
        subprocess.run(["npm", "run", "copy-assets"], cwd=upstream, env=build_env, check=True)
        first = Path(temporary) / "first.db"
        expected, snapshot, schema, metrics, library = produce(upstream, args.node, first)
        repeated, repeated_snapshot, repeated_schema, _, repeated_library = produce(upstream, args.node, Path(temporary) / "second.db")
        if (expected, snapshot, schema, library) != (repeated, repeated_snapshot, repeated_schema, repeated_library):
            raise RuntimeError("producer logical output is nondeterministic after sanitation")
        if args.check:
            manifest = json.loads((FIXTURE / "manifest.json").read_text())
            if manifest["producer"]["build"] != BUILD or manifest["producer"]["environment"] != PRODUCER_ENV or manifest["producer"]["home"] != "fresh empty temporary directory":
                raise RuntimeError("manifest producer build/environment mismatch")
            if manifest["producer"]["commit"] != actual or manifest["producer"]["node"] != runtime:
                raise RuntimeError("manifest producer identity mismatch")
            if manifest["producer"]["lockfile_sha256"] != hashlib.sha256((upstream / "package-lock.json").read_bytes()).hexdigest():
                raise RuntimeError("producer lockfile changed")
            for name, digest in manifest["sha256"].items():
                if hashlib.sha256((FIXTURE / name).read_bytes()).hexdigest() != digest:
                    raise RuntimeError(f"manifest hash mismatch: {name}")
            sources = {str(path.relative_to(FIXTURE)) for path in (FIXTURE / "source").rglob("*") if path.is_file()}
            if sources != {name for name in manifest["sha256"] if name.startswith("source/")}:
                raise RuntimeError("source file set changed")
            with sqlite3.connect(f"file:{FIXTURE / 'reference.db'}?mode=ro", uri=True) as committed:
                if snapshot != logical_snapshot(committed):
                    raise RuntimeError("full reference facts changed")
            if library != json.loads((FIXTURE / "library-expected.json").read_text()):
                raise RuntimeError("library answers changed")
            if expected != json.loads((FIXTURE / "expected.json").read_text()):
                raise RuntimeError("reference answers changed")
            if schema != (FIXTURE / "schema.sql").read_text():
                raise RuntimeError("reference schema changed")
            print("Pinned producer regenerated twice; logical facts, schema and committed answers match.")
            return
        shutil.copyfile(first, FIXTURE / "reference.db")
        write_json(FIXTURE / "expected.json", expected)
        write_json(FIXTURE / "library-expected.json", library)
        (FIXTURE / "schema.sql").write_text(schema)
        # Read vocabularies from the actual built producer; never claim these synthetic facts were extracted.
        vocabulary = json.loads(subprocess.check_output([args.node, "-e", f"const t=require({json.dumps(str(upstream / 'dist/types.js'))});console.log(JSON.stringify({{node_kinds:t.NODE_KINDS,edge_kinds:t.EDGE_KINDS}}))"], text=True, env={**PRODUCER_ENV, "HOME": temporary}))
        vocabulary["origin"] = "synthetic schema vocabulary; not producer extraction"
        vocabulary["nodes"] = [{"id": f"synthetic:{kind}", "kind": kind} for kind in vocabulary["node_kinds"]]
        vocabulary["edges"] = [{"source": "synthetic:function", "target": "synthetic:class", "kind": kind} for kind in vocabulary["edge_kinds"]]
        write_json(FIXTURE / "synthetic-contract.json", vocabulary)
        write_json(FIXTURE / "baseline.json", {"scope": "pinned portable CodeGraph indexing, library getCallers, and separate direct SQLite call query; not GraphNest or browser latency", "runtime": runtime, "platform": os.uname().sysname + " " + os.uname().machine, **metrics})
        names = ["reference.db", "expected.json", "library-expected.json", "schema.sql", "synthetic-contract.json"] + [str(path.relative_to(FIXTURE)) for path in sorted((FIXTURE / "source").rglob("*")) if path.is_file()]
        write_json(FIXTURE / "manifest.json", {
            "fixture": "polyglot-core",
            "producer": {"repository": "https://github.com/colbymchenry/codegraph", "commit": COMMIT, "version": "1.6.0", "node": NODE, "mode": "portable", "kernel": False, "build": BUILD, "environment": PRODUCER_ENV, "home": "fresh empty temporary directory", "lockfile_sha256": hashlib.sha256((upstream / "package-lock.json").read_bytes()).hexdigest()},
            "sanitation": "source mtimes zero; SQLite timestamp columns zero; temporary source root replaced by /fixture; backup checkpoint and VACUUM; no facts added",
            "determinism": "two independent real producer runs; all logical non-FTS tables and schema compared",
            "sha256": {name: hashlib.sha256((FIXTURE / name).read_bytes()).hexdigest() for name in names},
            "extracted_node_kinds": [row[0] for row in expected[0]["rows"]],
            "extracted_edge_kinds": [row[0] for row in expected[1]["rows"]],
        })
        print("Generated real reference twice, verified deterministic logical output, wrote sanitized fixture.")


if __name__ == "__main__":
    main()
