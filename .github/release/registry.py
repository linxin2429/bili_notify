"""Publish tested images without rebuilding or overwriting immutable versions."""

import argparse
import json
import re
import subprocess
import urllib.request

from release import run, version_key, write_outputs

IMMUTABLE_VERSION_RULE = "^[0-9]+[.][0-9]+[.][0-9]+$"


def require_immutable_versions(data):
    # These release repositories are public. Read their actual Hub policy without
    # granting the release token repository-admin privileges or changing settings.
    namespace, repository = data["image"].split("/")
    url = f"https://hub.docker.com/v2/namespaces/{namespace}/repositories/{repository}"
    with urllib.request.urlopen(url, timeout=20) as response:
        settings = json.load(response).get("immutable_tags_settings", {})
    # Require the documented canonical RE2 rule, not Python's interpretation of
    # arbitrary Hub regexes. Aliases and Cosign's sha256-* tags must stay mutable.
    if settings.get("enabled") is not True or settings.get("rules") != [
        IMMUTABLE_VERSION_RULE
    ]:
        raise ValueError(
            f"{data['image']} must enable Specific tags are immutable with the sole rule "
            f"{IMMUTABLE_VERSION_RULE}; publication is disabled until Hub enforces this policy"
        )


def labels(data):
    return {
        "org.opencontainers.image.source": f"https://github.com/{data['repo']}",
        "org.opencontainers.image.revision": data["sha"],
        "org.opencontainers.image.version": data["version"],
        "io.bili-notify.release.component": data["component"],
        "io.bili-notify.release.inputs": data["input_hash"],
        "io.bili-notify.release.tag": data["tag"],
    }


def verify_image(data, image):
    actual = json.loads(run("docker", "image", "inspect", image))[0]
    if actual["Os"] != "linux" or actual["Architecture"] != "amd64":
        raise ValueError("existing image has the wrong platform")
    for key, value in labels(data).items():
        if (actual["Config"].get("Labels") or {}).get(key) != value:
            raise ValueError(f"refusing to reuse an image with mismatched {key}")
    return actual


def recover(data, smoke_tag):
    image = f"{data['image']}:{data['version']}"
    pulled = subprocess.run(
        ["docker", "pull", "--platform", "linux/amd64", image],
        check=False,
        capture_output=True,
        text=True,
    )
    if pulled.returncode:
        # Authentication, transport and rate-limit errors are not missing tags.
        if "manifest unknown" in pulled.stderr.lower():
            return False
        raise RuntimeError(f"cannot check immutable image: {pulled.stderr}")
    actual = verify_image(data, image)
    run("docker", "tag", actual["Id"], smoke_tag)
    return True


def guard(data):
    require_immutable_versions(data)
    # Include aliases from partially completed and legacy releases, which have no
    # success record yet. Otherwise a failed newer release could be rolled back.
    image = f"{data['image']}:latest"
    pulled = subprocess.run(
        ["docker", "pull", "--platform", "linux/amd64", image],
        check=False,
        capture_output=True,
        text=True,
    )
    if pulled.returncode:
        if "manifest unknown" in pulled.stderr.lower():
            return
        raise RuntimeError(f"cannot check latest image: {pulled.stderr}")
    actual = json.loads(run("docker", "image", "inspect", image))[0]
    version = (actual["Config"].get("Labels") or {}).get(
        "org.opencontainers.image.version", ""
    )
    if not re.fullmatch(
        r"(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)", version
    ):
        raise ValueError(
            "latest image has no valid stable version label; cannot verify release order"
        )
    if version_key(data["version"]) < version_key(version):
        raise ValueError(
            f"refusing to roll back latest from {version} to {data['version']}"
        )


def push_tag(image_id, tag):
    run("docker", "tag", image_id, tag)
    output = run("docker", "push", tag)
    matches = re.findall(r"digest: (sha256:[0-9a-f]{64})", output)
    if not matches:
        raise ValueError(f"no digest returned while pushing {tag}")
    return matches[-1]


def existing_version_digest(data, smoke_tag, expected_id):
    if recover(data, smoke_tag + "-existing"):
        existing = verify_image(data, smoke_tag + "-existing")
        if expected_id != existing["Id"]:
            raise ValueError(
                "immutable version already exists with different image bytes"
            )
        digests = [
            item.split("@", 1)[1]
            for item in existing["RepoDigests"]
            if item.split("@", 1)[0] == data["image"]
        ]
        if len(digests) != 1:
            raise ValueError("cannot resolve the existing immutable image digest")
        return digests[0]
    return None


def immutable(data, smoke_tag):
    require_immutable_versions(data)
    actual = verify_image(data, smoke_tag)
    existing = existing_version_digest(data, smoke_tag, actual["Id"])
    if existing:
        return existing
    # The Hub policy, not this existence check, makes tag creation atomic against
    # other credentialed publishers. A losing push never overwrites the winner.
    try:
        return push_tag(actual["Id"], f"{data['image']}:{data['version']}")
    except subprocess.CalledProcessError:
        existing = existing_version_digest(data, smoke_tag, actual["Id"])
        if existing:
            return existing
        raise


def aliases(data, smoke_tag, digest):
    guard(data)
    actual = verify_image(data, smoke_tag)
    major, minor, _ = data["version"].split(".")
    for suffix in (f"{major}.{minor}", major, "latest"):
        pushed = push_tag(actual["Id"], f"{data['image']}:{suffix}")
        if pushed != digest:
            raise ValueError("published tags do not reference the tested image digest")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=["guard", "recover", "immutable", "aliases"])
    parser.add_argument("--file", required=True)
    parser.add_argument("--smoke-tag", required=True)
    parser.add_argument("--repo", required=True)
    parser.add_argument("--digest")
    args = parser.parse_args()
    with open(args.file) as source:
        data = json.load(source)
    data["repo"] = args.repo
    if args.command == "guard":
        guard(data)
    elif args.command == "recover":
        write_outputs({"recovered": recover(data, args.smoke_tag)})
    elif args.command == "immutable":
        write_outputs({"digest": immutable(data, args.smoke_tag)})
    else:
        aliases(data, args.smoke_tag, args.digest)


if __name__ == "__main__":
    main()
