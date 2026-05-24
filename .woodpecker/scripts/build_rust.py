import os
import sys

from wrenn import Capsule, StreamExitEvent, StreamStderrEvent, StreamStdoutEvent
from wrenn._git import GitCommandError

RUST_VERSION = os.getenv("RUST_VERSION", "1.95.0")
REPO_URL = "https://git.omukk.dev/wrenn/wrenn.git"
REPO_DIR = "/home/wrenn-user/wrenn"
BUILDS_DIR = os.path.join(os.path.dirname(__file__), "..", "..", "builds")


def read_envd_version(capsule: Capsule) -> str:
    content = capsule.files.read_bytes(f"{REPO_DIR}/envd-rs/Cargo.toml")
    for line in content.decode("utf-8").splitlines():
        stripped = line.strip()
        if stripped.startswith("version ="):
            return stripped.split("=", 1)[1].strip().strip('"')
    print("FAIL [version]: envd-rs/Cargo.toml has no package version", file=sys.stderr)
    sys.exit(1)


def run(capsule: Capsule, cmd: str, timeout: int = 30, envs={}) -> int:
    result = capsule.commands.run(cmd, timeout=timeout, envs=envs)
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


def build_rust(capsule: Capsule) -> bool:
    if run(capsule, f"mkdir -p {REPO_DIR}/builds") != 0:
        return False

    handle = capsule.commands.run(
        "make build-envd",
        background=True,
        cwd=REPO_DIR,
    )
    print(f"rust build started (pid={handle.pid}), streaming output...")

    exit_code = 0
    for event in capsule.commands.connect(handle.pid):
        if isinstance(event, StreamStdoutEvent):
            print(event.data, end="")
        elif isinstance(event, StreamStderrEvent):
            print(event.data, end="", file=sys.stderr)
        elif isinstance(event, StreamExitEvent):
            exit_code = event.exit_code

    if exit_code != 0:
        print(f"FAIL [rust build]: exit={exit_code}", file=sys.stderr)
        return False

    print("OK   [rust build]")
    return True


def download_artifacts(capsule: Capsule) -> bool:
    version = read_envd_version(capsule)
    remote_path = f"{REPO_DIR}/builds/envd"
    local_dir = os.path.normpath(BUILDS_DIR)
    local_name = f"envd-{version}"
    local_path = os.path.join(local_dir, local_name)
    os.makedirs(local_dir, exist_ok=True)

    print(f"Downloading envd as {local_name}...")
    with open(local_path, "wb") as f:
        for chunk in capsule.files.download_stream(remote_path):
            f.write(chunk)

    print(f"OK   [download {local_name}]")
    return True


def main() -> None:
    with Capsule(template="rust-1.95", wait=True, vcpus=4, memory_mb=4096) as capsule:
        print(f"Capsule: {capsule.capsule_id}")
        if not clone_repo(capsule):
            sys.exit(1)
        if not build_rust(capsule):
            sys.exit(1)
        if not download_artifacts(capsule):
            sys.exit(1)
        print("Done.")


if __name__ == "__main__":
    main()
