import os
import sys

from wrenn import Capsule, StreamExitEvent, StreamStderrEvent, StreamStdoutEvent
from wrenn._git import GitCommandError

GO_VERSION = os.getenv("GO_VERSION", "1.25.8")
REPO_URL = "https://git.omukk.dev/wrenn/wrenn.git"
REPO_DIR = "~/wrenn"
BUILDS_DIR = os.path.join(os.path.dirname(__file__), "..", "..", "builds")


def read_remote_version(capsule: Capsule, filename: str) -> str:
    content = capsule.files.read_bytes(f"{REPO_DIR}/{filename}")
    return content.decode("utf-8").strip()


def run(capsule: Capsule, cmd: str, timeout: int = 30) -> int:
    result = capsule.commands.run(cmd, timeout=timeout)
    if result.exit_code != 0:
        print(f"FAIL [{cmd.split()[0]}]: exit={result.exit_code}", file=sys.stderr)
        if result.stderr:
            print(result.stderr.strip(), file=sys.stderr)
        return result.exit_code
    print(f"OK   [{cmd.split()[0]}]")
    return 0


def clone_repo(capsule: Capsule) -> bool:
    try:
        capsule.git.clone(REPO_URL, REPO_DIR)
        print("OK   [git clone]")
        return True
    except GitCommandError as e:
        print(f"FAIL [git clone]: {e}", file=sys.stderr)
        return False


def build_go(capsule: Capsule) -> bool:
    command = "CGO_ENABLED=1 make build-cp build-agent"
    handle = capsule.commands.run(
        command,
        background=True,
        cwd=REPO_DIR,
        envs={
            "PATH": "/usr/local/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
        },
    )
    print(f"{command} started (pid={handle.pid}), streaming output...")

    exit_code = 0
    for event in capsule.commands.connect(handle.pid):
        if isinstance(event, StreamStdoutEvent):
            print(event.data, end="")
        elif isinstance(event, StreamStderrEvent):
            print(event.data, end="", file=sys.stderr)
        elif isinstance(event, StreamExitEvent):
            exit_code = event.exit_code

    if exit_code != 0:
        print(f"FAIL [go build]: exit={exit_code}", file=sys.stderr)
        return False
    print("OK   [go build]")
    return True


def download_artifacts(capsule: Capsule) -> bool:
    remote_dir = f"{REPO_DIR}/builds"
    entries = capsule.files.list(remote_dir, depth=1)
    files = [e for e in entries if e.type != "directory"]

    if not files:
        print("FAIL [download]: no files found in builds/", file=sys.stderr)
        return False

    local_dir = os.path.normpath(BUILDS_DIR)
    os.makedirs(local_dir, exist_ok=True)
    versions = {
        "wrenn-cp": read_remote_version(capsule, "VERSION_CP"),
        "wrenn-agent": read_remote_version(capsule, "VERSION_AGENT"),
    }

    for entry in files:
        name = entry.name or "unknown"
        remote_path = f"{remote_dir}/{name}"
        local_name = f"{name}-{versions[name]}" if name in versions else name
        local_path = os.path.join(local_dir, local_name)
        print(f"Downloading {name} as {local_name} ({entry.size or '?'} bytes)...")

        with open(local_path, "wb") as f:
            for chunk in capsule.files.download_stream(remote_path):
                f.write(chunk)

        print(f"OK   [download {local_name}]")

    return True


def main() -> None:
    with Capsule(template="go-1.25.8", wait=True, vcpus=4, memory_mb=4096) as capsule:
        print(f"Capsule: {capsule.capsule_id}")
        if not clone_repo(capsule):
            sys.exit(1)
        if not build_go(capsule):
            sys.exit(1)
        if not download_artifacts(capsule):
            sys.exit(1)
        print("Done.")


if __name__ == "__main__":
    main()
