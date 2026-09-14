"""Component release planning and durable success records (standard library only)."""

import argparse
import fnmatch
import hashlib
import json
import os
import re
import subprocess
import sys
import tempfile
from pathlib import Path

POLICY = Path(__file__).with_name("inputs.json")
RECORD = "release-record.json"
TAG = re.compile(
    r"(app|worker)/v((?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*))"
)
IMAGES = {"app": "dengxinlin/bili-notify", "worker": "dengxinlin/bili-notify-ai-worker"}


def run(*args, **kwargs):
    return subprocess.run(
        args, check=True, capture_output=True, text=True, **kwargs
    ).stdout.strip()


def parse_tag(tag):
    match = TAG.fullmatch(tag)
    if not match:
        raise ValueError("expected app/vMAJOR.MINOR.PATCH or worker/vMAJOR.MINOR.PATCH")
    return match.groups()


def version_key(version):
    return tuple(int(part) for part in version.split("."))


def includes(path, component, policy):
    matches = lambda patterns: any(
        fnmatch.fnmatchcase(path, pattern) for pattern in patterns
    )
    return matches(policy["shared"]) or (
        matches(policy[component]["include"])
        and not matches(policy[component]["exclude"])
    )


def fingerprint(sha, component, policy, repository="."):
    # Git tree entries include names, blob IDs and modes: deletions, renames and
    # executable-bit changes count, while an edit subsequently reverted does not.
    tree = subprocess.run(
        ["git", "-C", str(repository), "ls-tree", "-r", "-z", "--full-tree", sha],
        check=True,
        capture_output=True,
    ).stdout
    digest = hashlib.sha256()
    digest.update(json.dumps(policy, sort_keys=True).encode())
    for entry in tree.split(b"\0"):
        if entry:
            path = entry.split(b"\t", 1)[1].decode("utf-8")
            if includes(path, component, policy):
                digest.update(entry + b"\0")
    return digest.hexdigest()


def validate_source(tag):
    parse_tag(tag)
    sha = run("git", "rev-parse", "--verify", f"refs/tags/{tag}^{{commit}}")
    run("git", "merge-base", "--is-ancestor", sha, "refs/remotes/origin/main")
    if run("git", "rev-parse", "HEAD") != sha:
        raise ValueError("checkout does not match the requested release tag")
    return sha


def api(repo, endpoint, *args, **kwargs):
    return json.loads(run("gh", "api", f"repos/{repo}/{endpoint}", *args, **kwargs))


def releases(repo):
    # Pagination must not truncate the baseline when the other component has
    # published many more versions. API failures must propagate, never mean empty.
    pages = json.loads(
        run("gh", "api", "--paginate", "--slurp", f"repos/{repo}/releases?per_page=100")
    )
    return [release for page in pages for release in page]


def validate_record(record, tag):
    component, version = parse_tag(tag)
    if (
        record.get("schema") != 1
        or record.get("tag") != tag
        or record.get("component") != component
        or record.get("version") != version
        or record.get("image") != IMAGES[component]
        or not re.fullmatch(r"[0-9a-f]{40}", record.get("sha", ""))
        or not re.fullmatch(r"sha256:[0-9a-f]{64}", record.get("digest", ""))
        or not re.fullmatch(r"[0-9a-f]{64}", record.get("input_hash", ""))
        or not isinstance(record.get("input_version"), int)
    ):
        raise ValueError(f"invalid success record for {tag}")
    actual = run("git", "rev-parse", "--verify", f"refs/tags/{tag}^{{commit}}")
    if actual != record["sha"]:
        raise ValueError(f"success record source does not match tag {tag}")
    return record


def latest_records(repo, all_releases):
    result = {}
    for component in IMAGES:
        candidates = []
        for release in all_releases:
            tag = release["tag_name"]
            if release["draft"] or release["prerelease"] or not TAG.fullmatch(tag):
                continue
            kind, version = parse_tag(tag)
            assets = [a for a in release["assets"] if a["name"] == RECORD]
            if kind == component and assets:
                candidates.append((version_key(version), release, assets[0]))
        if candidates:
            _, release, asset = max(candidates, key=lambda item: item[0])
            record = api(
                repo,
                f"releases/assets/{asset['id']}",
                "-H",
                "Accept: application/octet-stream",
            )
            result[component] = validate_record(record, release["tag_name"])
    return result


def decide(tag, sha, input_hash, baseline, force=False):
    _, version = parse_tag(tag)
    if baseline:
        current, previous = version_key(version), version_key(baseline["version"])
        if current < previous:
            raise ValueError(f"version rollback: {version} < {baseline['version']}")
        if current == previous:
            if sha != baseline["sha"] or input_hash != baseline["input_hash"]:
                raise ValueError(
                    "published version does not match the requested source/inputs"
                )
            return False, "already published"
        if input_hash == baseline["input_hash"] and not force:
            return False, "no component input changes"
    return (
        True,
        "forced"
        if force
        else "changed inputs"
        if baseline
        else "first component release",
    )


def plan(tag, repo, force=False):
    component, version = parse_tag(tag)
    sha = validate_source(tag)
    policy = json.loads(POLICY.read_text())
    records = latest_records(repo, releases(repo))
    input_hash = fingerprint(sha, component, policy)
    publish, reason = decide(tag, sha, input_hash, records.get(component), force)
    return {
        "schema": 1,
        "component": component,
        "version": version,
        "tag": tag,
        "sha": sha,
        "image": IMAGES[component],
        "input_hash": input_hash,
        "input_version": policy["version"],
        "publish": publish,
        "reason": reason,
        "dockerfile": "Dockerfile" if component == "app" else "worker/Dockerfile",
        "smoke_target": "docker-smoke-image"
        if component == "app"
        else "worker-docker-smoke-image",
        "latest": records,
    }


def write_outputs(values):
    if os.environ.get("GITHUB_OUTPUT"):
        with open(os.environ["GITHUB_OUTPUT"], "a") as output:
            for key, value in values.items():
                if isinstance(value, (str, bool, int)):
                    output.write(
                        f"{key}={str(value).lower() if isinstance(value, bool) else value}\n"
                    )


def summarize(data, published=False):
    lines = [
        f"### {data['tag']}",
        "",
        "Published successfully" if published else data["reason"],
        "",
    ]
    for component in IMAGES:
        record = data["latest"].get(component)
        lines.append(
            f"- {component}: `{record['image']}:{record['version']}` (`{record['digest']}`)"
            if record
            else f"- {component}: no component release record yet; existing legacy images remain available"
        )
    lines += [
        "",
        "These are the latest successful versions, not a protocol compatibility guarantee.",
        "",
    ]
    print("\n".join(lines))
    if os.environ.get("GITHUB_STEP_SUMMARY"):
        with open(os.environ["GITHUB_STEP_SUMMARY"], "a") as summary:
            summary.write("\n".join(lines))


def complete(data, repo, digest):
    if not re.fullmatch(r"sha256:[0-9a-f]{64}", digest):
        raise ValueError("invalid published image digest")
    record = {
        key: data[key]
        for key in (
            "schema",
            "component",
            "version",
            "tag",
            "sha",
            "image",
            "input_hash",
            "input_version",
        )
    }
    record["digest"] = digest
    validate_record(record, data["tag"])
    current_releases = releases(repo)
    existing = next(
        (item for item in current_releases if item["tag_name"] == data["tag"]), None
    )
    asset = None
    if existing:
        if existing["prerelease"]:
            raise ValueError(
                "component success records require a stable GitHub Release"
            )
        asset = next((a for a in existing["assets"] if a["name"] == RECORD), None)
        if asset:
            previous = api(
                repo,
                f"releases/assets/{asset['id']}",
                "-H",
                "Accept: application/octet-stream",
            )
            if previous != record:
                raise ValueError("refusing to overwrite a different success record")
    else:
        body = {
            "tag_name": data["tag"],
            "name": data["tag"],
            "draft": True,
            "make_latest": "false",
            "body": f"Verified image: `{data['image']}:{data['version']}`\n\nDigest: `{digest}`",
        }
        existing = api(
            repo, "releases", "--method", "POST", "--input", "-", input=json.dumps(body)
        )
    # Drafts never count as baselines. Upload before publishing also supports
    # repositories with immutable GitHub Releases enabled. Both interrupted steps
    # can be resumed without replacing the record or user-authored release notes.
    if not asset:
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / RECORD
            path.write_text(json.dumps(record, indent=2) + "\n")
            run("gh", "release", "upload", data["tag"], str(path), "--repo", repo)
    if existing["draft"]:
        api(
            repo,
            f"releases/{existing['id']}",
            "--method",
            "PATCH",
            "--input",
            "-",
            input=json.dumps({"draft": False, "make_latest": "false"}),
        )
    data["latest"] = latest_records(repo, releases(repo))
    summarize(data, published=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=["plan", "complete"])
    parser.add_argument("--tag")
    parser.add_argument("--repo", default=os.environ.get("GITHUB_REPOSITORY"))
    parser.add_argument("--force", action="store_true")
    parser.add_argument("--file", required=True)
    parser.add_argument("--digest")
    args = parser.parse_args()
    if not args.repo:
        parser.error("--repo or GITHUB_REPOSITORY is required")
    if args.command == "plan":
        data = plan(args.tag, args.repo, args.force)
        Path(args.file).write_text(json.dumps(data, indent=2) + "\n")
        write_outputs(data)
        summarize(data)
    else:
        complete(json.loads(Path(args.file).read_text()), args.repo, args.digest)


if __name__ == "__main__":
    try:
        main()
    except (
        ValueError,
        KeyError,
        TypeError,
        OSError,
        subprocess.CalledProcessError,
    ) as error:
        print(f"Release failed: {error}", file=sys.stderr)
        if isinstance(error, subprocess.CalledProcessError):
            print(error.stderr, file=sys.stderr)
        sys.exit(1)
