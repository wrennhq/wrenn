import base64
import os
import sys

from wrenn import Capsule

REPO_URL = "https://git.omukk.dev/wrenn/wrenn.git"
REPO_DIR = "wrenn-releases"
CAPSULE_OUTPUT = "/tmp/release_notes.md"
LOCAL_OUTPUT = os.path.join(os.path.dirname(__file__), "..", "release_notes.md")

DEFAULT_MODEL = "opencode/big-pickle"

RELEASE_NOTES_EXAMPLE = """
## What's new
Sandbox HTTP proxying, terminal reliability, and auth robustness improvements.

### Proxy
- Fixed redirect loops for apps served inside sandboxes (Python HTTP server, Jupyter, etc.)
- Proxy traffic no longer interferes with terminal and exec connections
- Services that take a moment to start up inside a sandbox are now retried instead of immediately failing

### Terminal (PTY)
- Terminal input is no longer blocked by slow network conditions — fast typing no longer causes timeouts or disconnects
- Input bursts are coalesced into fewer round trips — lower latency under fast typing

### Authentication
- WebSocket connections now authenticate correctly for both SDK clients (header-based) and browser clients (message-based)

### Bug Fixes
- Fixed crash in envd when a process exits without a PTY
- Fixed goroutine leak on sandbox pause

### Others
- Version bump
""".strip()


def run(capsule: Capsule, cmd: str, cwd: str | None = None, timeout: int = 30) -> int:
    result = capsule.commands.run(cmd, cwd=cwd, timeout=timeout)
    if result.exit_code != 0:
        print(f"FAIL [{cmd.split()[0]}]: exit={result.exit_code}", file=sys.stderr)
        if result.stderr:
            print(result.stderr.strip(), file=sys.stderr)
        return result.exit_code
    print(f"OK   [{cmd.split()[0]}]")
    return 0


def generate_release_notes(
    capsule: Capsule,
    output_path: str,
    model: str,
) -> None:
    prompt = f"""
    You are inside a cloned git repository at:

    {REPO_DIR}

    Generate release notes for the latest tagged version of this software project.

    Before writing anything, inspect the repository yourself using git commands.

    You MUST determine:
        1. The latest version tag.
        2. The previous version tag, if one exists.
        3. The commits between the previous tag and the latest tag.
        4. The files and areas changed between those tags.

    Use commands like:

    git tag --sort=-version:refname

    Compare the newest tag against the previous tag:

    git log PREVIOUS_TAG..LATEST_TAG --pretty=format:'%s (%h)'
    git diff PREVIOUS_TAG..LATEST_TAG --stat
    git diff PREVIOUS_TAG..LATEST_TAG --name-only

    If there is only one tag, inspect the latest tag with:

    Do not rely on any pre-injected commit list or diff summary.
    You must inspect the git history yourself.

    Write the release notes in plain, friendly language that any developer can understand
    without deep knowledge of the codebase.

    Avoid jargon like "goroutine", "PTY", "envd", or internal function names.
    Describe what the change means for the user instead.

    Group related changes under headings that reflect what actually changed.
    Only include sections that are relevant to the actual changes.
    Do not include CI/CD-only changes.

    Start with:

    ## What's New

    The very next line must be a single short summary sentence.

    Keep each bullet point to one clear sentence.

    Here is an example of the style to aim for — not a template to copy:

    {RELEASE_NOTES_EXAMPLE}

    Output only the final markdown.
    No intro.
    No explanation.
    No conversational filler.
    No acknowledgments.
    No "I checked the logs" text.
    No thoughts.
    """.strip()

    prompt_b64 = base64.b64encode(prompt.encode("utf-8")).decode("utf-8")

    write_prompt_cmd = f"echo '{prompt_b64}' | base64 -d > /tmp/oc_prompt.txt"

    result = capsule.commands.run(
        write_prompt_cmd,
        cwd=REPO_DIR,
        timeout=10,
    )
    if result.exit_code != 0:
        print(f"FAIL [write prompt]: {result.stderr}", file=sys.stderr)
        sys.exit(1)

    def run_opencode_with_model(target_model: str) -> int:
        env = ""
        if "zhipu" in target_model.lower():
            env = f"ZHIPU_API_KEY={os.environ.get('ZHIPU_API_KEY', '')}"

        raw_output_path = "/tmp/opencode_raw.txt"
        cmd = (
            f"{env} "
            f"~/.opencode/bin/opencode run "
            f'"Read the attached file and generate the release notes. Output ONLY markdown." '
            f"--model {target_model} "
            f"--file /tmp/oc_prompt.txt "
            f"> {raw_output_path}"
        )

        cmd_result = capsule.commands.run(cmd, cwd=REPO_DIR, timeout=300)
        if cmd_result.exit_code != 0:
            print(
                f"FAIL [opencode via {target_model}]: exit={cmd_result.exit_code}",
                file=sys.stderr,
            )
            print(f"STDOUT:\n{cmd_result.stdout}", file=sys.stderr)
            print(f"STDERR:\n{cmd_result.stderr}", file=sys.stderr)

        clean_cmd = (
            f"awk 'found || /^## What.s [Nn]ew/ {{ found=1; print }}' "
            f"{raw_output_path} > {output_path}"
        )

        clean_result = capsule.commands.run(clean_cmd, cwd=REPO_DIR, timeout=10)
        if clean_result.exit_code != 0:
            print(f"FAIL [clean output]: {clean_result.stderr}", file=sys.stderr)
            return clean_result.exit_code

        check_result = capsule.commands.run(
            f"grep -q '^## What.s New' {output_path}",
            cwd=REPO_DIR,
            timeout=10,
        )
        if check_result.exit_code != 0:
            print(
                "FAIL: Could not find release notes heading in opencode output",
                file=sys.stderr,
            )
            print(cmd_result.stdout, file=sys.stderr)
            print(cmd_result.stderr, file=sys.stderr)
            return 1

        return cmd_result.exit_code

    exit_status = run_opencode_with_model(model)

    if exit_status != 0:
        fallback_model = "opencode/big-pickle"
        if model != fallback_model:
            print(
                f"\n[!] Model {model} failed. Falling back to {fallback_model}...",
                file=sys.stderr,
            )
            exit_status = run_opencode_with_model(fallback_model)
            if exit_status != 0:
                print("FAIL: Fallback model also failed. Exiting.", file=sys.stderr)
                sys.exit(1)
        else:
            sys.exit(1)

    result = capsule.commands.run(f"cat {output_path}")
    print(result.stdout)
    if result.stderr:
        print(result.stderr)

    print(f"OK   [opencode] release notes written to {output_path}")


def download_release_notes(capsule: Capsule) -> None:
    local_path = os.path.normpath(LOCAL_OUTPUT)
    os.makedirs(os.path.dirname(local_path), exist_ok=True)

    print("Downloading release notes from capsule...")
    content = capsule.files.read_bytes(CAPSULE_OUTPUT)
    with open(local_path, "wb") as f:
        f.write(content)

    print(f"OK   [download] release notes → {local_path}")
    print(content.decode("utf-8", errors="replace"))


def main() -> None:
    model = os.environ.get("OPENCODE_MODEL", DEFAULT_MODEL)

    with Capsule(template="opencode", wait=True) as capsule:
        print(f"Capsule: {capsule.capsule_id}")

        capsule.git.clone(
            REPO_URL,
            REPO_DIR,
        )
        print("OK   [git clone]")

        # Note: This simply creates the directory string safely
        output_path = os.path.normpath(CAPSULE_OUTPUT)

        generate_release_notes(capsule, output_path, model)

        download_release_notes(capsule)


if __name__ == "__main__":
    main()
