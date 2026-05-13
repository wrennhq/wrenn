import os
import sys
from pathlib import Path

import httpx

GITHUB_REPO = "wrennhq/wrenn"
GITHUB_API = "https://api.github.com"
GITHUB_UPLOADS = "https://uploads.github.com"
BUILDS_DIR = "builds"
VERSION_FILE = "VERSION_CP"
NOTES_FILE = os.path.join(".woodpecker", "release_notes.md")


def main() -> None:
    token = os.environ["GITHUB_TOKEN"]

    with open(VERSION_FILE) as f:
        version = f.read().strip()
    tag = f"v{version}"

    release_notes = ""
    if os.path.exists(NOTES_FILE):
        with open(NOTES_FILE) as f:
            release_notes = f.read()

    headers = {
        "Authorization": f"token {token}",
        "Accept": "application/vnd.github+json",
        "X-GitHub-Api-Version": "2022-11-28",
    }

    client = httpx.Client(headers=headers, timeout=60)

    print(f"Creating GitHub release for {tag}...")
    resp = client.post(
        f"{GITHUB_API}/repos/{GITHUB_REPO}/releases",
        json={
            "tag_name": tag,
            "name": tag,
            "body": release_notes,
            "draft": False,
            "prerelease": False,
        },
    )
    if resp.status_code == 422:
        print(f"WARN [create release]: release for {tag} already exists, skipping")
        data = resp.json()
        errors = data.get("errors", [])
        if errors:
            existing_url = errors[0].get("documentation_url", "")
            print(f"  See: {existing_url}")
        client.close()
        return
    if resp.status_code != 201:
        print(f"FAIL [create release]: {resp.status_code} {resp.text}", file=sys.stderr)
        client.close()
        sys.exit(1)

    release_data = resp.json()
    release_id = release_data["id"]
    release_url = release_data.get("html_url", "")
    print(f"OK   [create release] id={release_id}")

    builds_path = Path(BUILDS_DIR)
    if not builds_path.exists():
        print(f"No {BUILDS_DIR}/ directory found, skipping asset upload")
        client.close()
        print(f"Release published: {release_url}")
        return

    upload_headers = {
        **headers,
        "Content-Type": "application/octet-stream",
    }

    for artifact in sorted(builds_path.iterdir()):
        if artifact.is_dir():
            continue
        print(f"Uploading {artifact.name}...")

        with open(artifact, "rb") as f:
            data = f.read()

        resp = client.post(
            f"{GITHUB_UPLOADS}/repos/{GITHUB_REPO}/releases/{release_id}/assets",
            params={"name": artifact.name},
            headers=upload_headers,
            content=data,
        )
        if resp.status_code != 201:
            print(
                f"WARN [upload {artifact.name}]: {resp.status_code} {resp.text}",
                file=sys.stderr,
            )
        else:
            print(f"OK   [upload {artifact.name}]")

    client.close()
    print(f"Release published: {release_url}")


if __name__ == "__main__":
    main()
