import os
import sys

from wrenn import Capsule, StreamExitEvent, StreamStderrEvent, StreamStdoutEvent
from wrenn._git import GitCommandError

RUST_VERSION = os.getenv("RUST_VERSION", "1.95.0")
REPO_URL = "https://git.omukk.dev/wrenn/wrenn.git"
REPO_DIR = "/opt/wrenn"
BUILDS_DIR = os.path.join(os.path.dirname(__file__), "..", "..", "builds")
RUST_PATH = (
    "/root/.cargo/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
)


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


def install_rust(capsule: Capsule) -> bool:
    if run(capsule, "apt update", timeout=120) != 0:
        return False
    if (
        run(
            capsule,
            "apt install -y make build-essential file curl musl-tools protobuf-compiler",
            timeout=300,
        )
        != 0
    ):
        return False
    if (
        run(
            capsule,
            f"curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs | sh -s -- -y --profile minimal --default-toolchain {RUST_VERSION}",
            timeout=300,
        )
        != 0
    ):
        return False
    if (
        run(
            capsule,
            "/root/.cargo/bin/rustup target add x86_64-unknown-linux-musl",
            timeout=120,
        )
        != 0
    ):
        return False

    result = capsule.commands.run("/root/.cargo/bin/rustc --version")
    print(result.stdout.strip())
    return result.exit_code == 0


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
        envs={"PATH": RUST_PATH},
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
    with Capsule(wait=True, vcpus=4, memory_mb=4096) as capsule:
        print(f"Capsule: {capsule.capsule_id}")
        if not install_rust(capsule):
            sys.exit(1)
        if not clone_repo(capsule):
            sys.exit(1)
        if not build_rust(capsule):
            sys.exit(1)
        if not download_artifacts(capsule):
            sys.exit(1)
        print("Done.")


if __name__ == "__main__":
    main()
