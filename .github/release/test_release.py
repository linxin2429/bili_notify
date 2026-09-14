"""Offline tests; external publishing boundaries are always mocked."""

import json
import subprocess
import tempfile
import unittest
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path
from unittest.mock import patch

import registry
import release

SHA = "a" * 40
HASH = "b" * 64
DIGEST = "sha256:" + "c" * 64


def record(component="app", version="1.2.0"):
    return {
        "schema": 1,
        "component": component,
        "version": version,
        "tag": f"{component}/v{version}",
        "sha": SHA,
        "input_hash": HASH,
        "input_version": 1,
        "digest": DIGEST,
        "image": release.IMAGES[component],
    }


def image(data):
    return {
        "Id": "sha256:" + "d" * 64,
        "Os": "linux",
        "Architecture": "amd64",
        "Config": {"Labels": registry.labels(data)},
        "RepoDigests": [f"{data['image']}@{DIGEST}"],
    }


class ReleaseTests(unittest.TestCase):
    def test_tag_validation(self):
        cases = [
            "v1.2.3",
            "app/v1.2",
            "worker/v01.2.3",
            "app/v1.2.3-rc.1",
            "app/v1.2.3\n",
            "../app/v1.2.3",
            "app/v1.2.3;id",
            "",
        ]
        for tag in cases:
            with self.subTest(tag=tag), self.assertRaises(ValueError):
                release.parse_tag(tag)
        for tag, want in [
            ("app/v0.0.0", ("app", "0.0.0")),
            ("worker/v12.34.56", ("worker", "12.34.56")),
        ]:
            with self.subTest(tag=tag):
                self.assertEqual(release.parse_tag(tag), want)

    def test_input_boundaries(self):
        policy = json.loads(release.POLICY.read_text())
        cases = [
            ("service/engine.go", True, False),
            ("state/migrations/001.sql", True, False),
            ("go.mod", True, False),
            ("Dockerfile", True, False),
            ("internal/aiworkerpb/worker.pb.go", True, False),
            ("web/ui/src/pages/HistoryPage.tsx", True, False),
            ("web/ui/package-lock.json", True, False),
            ("web/ui/public/icon.svg", True, False),
            ("web/ui/test-workers.ts", True, False),
            ("worker/Dockerfile", False, True),
            ("worker/requirements.lock", False, True),
            ("worker/bili_ai_worker/server.py", False, True),
            ("worker/ai/v1/worker_pb2.py", False, True),
            ("api/ai/v1/worker.proto", True, True),
            (".dockerignore", True, True),
            (".github/release/inputs.json", True, True),
            (".github/workflows/release-component.yml", True, True),
            ("README.md", False, False),
            ("compose.yaml", False, False),
            ("service/engine_test.go", False, False),
            ("worker/tests/test_server.py", False, False),
            ("worker/requirements-dev.lock", False, False),
            ("web/ui/src/pages/HistoryPage.test.tsx", False, False),
            ("web/ui/e2e/responsive.spec.ts-snapshots/view.png", False, False),
            ("web/testdata/contracts/overview.json", False, False),
            (".github/release/test_release.py", False, False),
        ]
        for path, app, worker in cases:
            with self.subTest(path=path):
                self.assertEqual(release.includes(path, "app", policy), app)
                self.assertEqual(release.includes(path, "worker", policy), worker)

    def test_release_decisions(self):
        cases = [
            ("first", "1.0.0", HASH, None, False, True),
            ("unchanged", "1.3.0", HASH, record(), False, False),
            ("forced", "1.3.0", HASH, record(), True, True),
            ("changed", "1.3.0", "e" * 64, record(), False, True),
            ("completed rerun", "1.2.0", HASH, record(), False, False),
            ("force cannot overwrite", "1.2.0", HASH, record(), True, False),
            (
                "numeric version",
                "1.10.0",
                "e" * 64,
                record(version="1.9.0"),
                False,
                True,
            ),
        ]
        for name, version, inputs, baseline, force, want in cases:
            with self.subTest(name=name):
                got, _ = release.decide(f"app/v{version}", SHA, inputs, baseline, force)
                self.assertEqual(got, want)
        for name, tag, sha, inputs in [
            ("rollback", "app/v1.1.0", SHA, HASH),
            ("moved tag", "app/v1.2.0", "e" * 40, HASH),
            ("changed published inputs", "app/v1.2.0", SHA, "e" * 64),
        ]:
            with self.subTest(name=name), self.assertRaises(ValueError):
                release.decide(tag, sha, inputs, record(), True)

    def test_tree_changes(self):
        # Exercise real Git trees in isolated repositories; run cases concurrently.
        cases = [
            ("add", True),
            ("delete", True),
            ("rename", True),
            ("mode", True),
            ("revert", False),
            ("docs", False),
        ]

        def check(case):
            operation, expected = case
            with tempfile.TemporaryDirectory() as directory:
                root = Path(directory)

                def git(*args):
                    return subprocess.run(
                        ["git", "-C", directory, *args],
                        check=True,
                        capture_output=True,
                        text=True,
                    ).stdout.strip()

                git("init", "-q")
                git("config", "user.email", "test@example.invalid")
                git("config", "user.name", "Release Test")
                (root / "main.go").write_text("package main\n")
                git("add", ".")
                git("commit", "-qm", "initial")
                before = git("ls-tree", "-rz", "HEAD")
                if operation == "add":
                    (root / "new.go").write_text("package main\n")
                elif operation == "delete":
                    (root / "main.go").unlink()
                elif operation == "rename":
                    (root / "main.go").rename(root / "renamed.go")
                elif operation == "mode":
                    (root / "main.go").chmod(0o755)
                elif operation == "revert":
                    (root / "main.go").write_text("changed\n")
                    git("add", ".")
                    git("commit", "-qm", "change")
                    (root / "main.go").write_text("package main\n")
                else:
                    (root / "README.md").write_text("documentation\n")
                git("add", "-A")
                tree = git("write-tree")
                return operation, before, git("ls-tree", "-rz", tree), expected

        with ThreadPoolExecutor() as executor:
            for operation, before, after, expected in executor.map(check, cases):
                with self.subTest(operation=operation):
                    policy = json.loads(release.POLICY.read_text())
                    with patch("release.subprocess.run") as command:
                        command.return_value.stdout = before.encode()
                        old = release.fingerprint(SHA, "app", policy)
                        command.return_value.stdout = after.encode()
                        new = release.fingerprint(SHA, "app", policy)
                    self.assertEqual(old != new, expected)

    def test_baseline_selection(self):
        items = [
            {"tag_name": "app/v1.9.0", "assets": [{"name": release.RECORD, "id": 1}]},
            {"tag_name": "app/v1.10.0", "assets": [{"name": release.RECORD, "id": 2}]},
            {"tag_name": "app/v1.11.0", "assets": []},  # partial publication
            {
                "tag_name": "worker/v0.2.0",
                "assets": [{"name": release.RECORD, "id": 3}],
            },
            {"tag_name": "v9.0.0", "assets": []},
        ]
        for item in items:
            item.update(draft=False, prerelease=False)
        with (
            patch(
                "release.api",
                side_effect=[record(version="1.10.0"), record("worker", "0.2.0")],
            ),
            patch("release.run", return_value=SHA),
        ):
            got = release.latest_records("owner/repo", items)
        self.assertEqual(got["app"]["version"], "1.10.0")
        self.assertEqual(got["worker"]["version"], "0.2.0")

    def test_baseline_failures_propagate(self):
        for error in [
            subprocess.CalledProcessError(1, "gh", stderr="network error"),
            ValueError("malformed JSON"),
        ]:
            with (
                self.subTest(error=error),
                patch("release.run", side_effect=error),
                self.assertRaises(type(error)),
            ):
                release.releases("owner/repo")
        for key, value in [
            ("digest", "bad"),
            ("sha", "bad"),
            ("schema", 2),
            ("image", "wrong/repo"),
            ("tag", "worker/v1.2.0"),
        ]:
            with self.subTest(key=key), self.assertRaises(ValueError):
                release.validate_record(dict(record(), **{key: value}), "app/v1.2.0")
        with patch("release.run", return_value="e" * 40), self.assertRaises(ValueError):
            release.validate_record(record(), "app/v1.2.0")

    def test_source_must_be_on_main_and_match_checkout(self):
        for results in [
            [SHA, subprocess.CalledProcessError(1, "git merge-base")],
            [SHA, "", "e" * 40],
        ]:
            with (
                self.subTest(results=results),
                patch("release.run", side_effect=results),
                self.assertRaises((ValueError, subprocess.CalledProcessError)),
            ):
                release.validate_source("app/v1.2.0")

    def test_success_marker_is_last_and_not_overwritten(self):
        cases = [
            ("new", False, False, False),
            ("interrupted upload", True, True, False),
            ("interrupted publish", True, True, True),
            ("existing notes", True, False, False),
            ("completed", True, False, True),
        ]
        for name, exists, draft, has_asset in cases:
            data = dict(record(), latest={})
            item = {
                "id": 9,
                "tag_name": data["tag"],
                "draft": draft,
                "prerelease": False,
                "assets": [{"name": release.RECORD, "id": 1}] if has_asset else [],
            }
            events = []

            def api(repo, endpoint, *args, events=events, item=item, **kwargs):
                if endpoint == "releases/assets/1":
                    return record()
                body = json.loads(kwargs["input"])
                if endpoint == "releases":
                    self.assertTrue(body["draft"])
                    events.append("create draft")
                    return dict(item, draft=True)
                self.assertEqual(endpoint, "releases/9")
                self.assertFalse(body["draft"])
                events.append("publish")
                return dict(item, draft=False)

            def upload(*args, events=events):
                self.assertEqual(args[:3], ("gh", "release", "upload"))
                self.assertNotIn("--clobber", args)
                self.assertEqual(json.loads(Path(args[4]).read_text()), record())
                events.append("upload")

            with (
                self.subTest(name=name),
                patch("release.validate_record"),
                patch("release.releases", return_value=[item] if exists else []),
                patch("release.api", side_effect=api),
                patch("release.run", side_effect=upload),
                patch("release.latest_records", return_value={}),
                patch("release.summarize"),
            ):
                release.complete(data, "owner/repo", DIGEST)
                expected = [] if exists else ["create draft"]
                if not has_asset:
                    expected.append("upload")
                if draft or not exists:
                    expected.append("publish")
                self.assertEqual(events, expected)
        with (
            patch("release.validate_record"),
            patch("release.releases", return_value=[item]),
            patch(
                "release.api", return_value=dict(record(), digest="sha256:" + "e" * 64)
            ),
            self.assertRaises(ValueError),
        ):
            release.complete(data, "owner/repo", DIGEST)


class RegistryTests(unittest.TestCase):
    def setUp(self):
        self.data = dict(record(), repo="owner/repo")

    def test_recovery_boundary(self):
        for error, missing in [
            ("manifest unknown", True),
            ("unauthorized", False),
            ("i/o timeout", False),
            ("toomanyrequests", False),
        ]:
            with self.subTest(error=error), patch("registry.subprocess.run") as command:
                command.return_value = subprocess.CompletedProcess([], 1, "", error)
                if missing:
                    self.assertFalse(registry.recover(self.data, "local:test"))
                else:
                    with self.assertRaises(RuntimeError):
                        registry.recover(self.data, "local:test")
        with (
            patch("registry.subprocess.run") as command,
            patch("registry.verify_image", return_value=image(self.data)),
            patch("registry.run") as run,
        ):
            command.return_value.returncode = 0
            self.assertTrue(registry.recover(self.data, "local:test"))
            self.assertEqual(run.call_args.args[:2], ("docker", "tag"))

    def test_image_identity(self):
        for key in registry.labels(self.data):
            actual = image(self.data)
            actual["Config"]["Labels"][key] = "wrong"
            with (
                self.subTest(key=key),
                patch("registry.run", return_value=json.dumps([actual])),
                self.assertRaises(ValueError),
            ):
                registry.verify_image(self.data, "local:test")
        actual = image(self.data)
        actual["Architecture"] = "arm64"
        with (
            patch("registry.run", return_value=json.dumps([actual])),
            self.assertRaises(ValueError),
        ):
            registry.verify_image(self.data, "local:test")

    def test_immutable_publication_and_resume(self):
        for exists in [False, True]:
            with (
                self.subTest(exists=exists),
                patch("registry.verify_image", return_value=image(self.data)),
                patch("registry.recover", return_value=exists),
                patch("registry.push_tag", return_value=DIGEST) as push,
            ):
                self.assertEqual(registry.immutable(self.data, "local:test"), DIGEST)
                self.assertEqual(push.call_count, 0 if exists else 1)
        with (
            patch(
                "registry.verify_image",
                side_effect=[image(self.data), dict(image(self.data), Id="different")],
            ),
            patch("registry.recover", return_value=True),
            self.assertRaises(ValueError),
        ):
            registry.immutable(self.data, "local:test")

    def test_alias_retry_and_digest_validation(self):
        guard = patch("registry.guard")
        guard.start()
        self.addCleanup(guard.stop)
        for fail_at in [0, 1, 2]:
            with (
                self.subTest(fail_at=fail_at),
                patch("registry.verify_image", return_value=image(self.data)),
                patch(
                    "registry.push_tag",
                    side_effect=[DIGEST] * fail_at + [ValueError("push failed")],
                ),
                self.assertRaises(ValueError),
            ):
                registry.aliases(self.data, "local:test", DIGEST)
        with (
            patch("registry.verify_image", return_value=image(self.data)),
            patch("registry.push_tag", return_value=DIGEST) as push,
        ):
            registry.aliases(self.data, "local:test", DIGEST)
            self.assertEqual(
                [call.args[1] for call in push.call_args_list],
                [f"{self.data['image']}:{tag}" for tag in ["1.2", "1", "latest"]],
            )
        with (
            patch("registry.verify_image", return_value=image(self.data)),
            patch("registry.push_tag", return_value="sha256:" + "e" * 64),
            self.assertRaises(ValueError),
        ):
            registry.aliases(self.data, "local:test", DIGEST)

    def test_latest_version_guard(self):
        cases = [
            ("1.1.0", False),
            ("1.2.0", False),
            ("1.3.0", True),
            ("1.10.0", True),
            ("", True),
            ("dev", True),
        ]
        for version, fails in cases:
            actual = image(self.data)
            actual["Config"]["Labels"]["org.opencontainers.image.version"] = version
            with (
                self.subTest(version=version),
                patch("registry.subprocess.run") as pull,
                patch("registry.run", return_value=json.dumps([actual])),
            ):
                pull.return_value.returncode = 0
                if fails:
                    with self.assertRaises(ValueError):
                        registry.guard(self.data)
                else:
                    registry.guard(self.data)
        for message, fails in [("manifest unknown", False), ("unauthorized", True)]:
            with (
                self.subTest(message=message),
                patch("registry.subprocess.run") as pull,
            ):
                pull.return_value = subprocess.CompletedProcess([], 1, "", message)
                if fails:
                    with self.assertRaises(RuntimeError):
                        registry.guard(self.data)
                else:
                    registry.guard(self.data)

    def test_push_requires_digest(self):
        for output, valid in [
            (f"1.2.0: digest: {DIGEST} size: 123", True),
            ("unexpected output", False),
        ]:
            with (
                self.subTest(output=output),
                patch("registry.run", side_effect=["", output]),
            ):
                if valid:
                    self.assertEqual(
                        registry.push_tag("image-id", "repo:1.2.0"), DIGEST
                    )
                else:
                    with self.assertRaises(ValueError):
                        registry.push_tag("image-id", "repo:1.2.0")


if __name__ == "__main__":
    unittest.main()
