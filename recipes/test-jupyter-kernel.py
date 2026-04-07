#!/usr/bin/env python3
import argparse
import json
import sys
import uuid

try:
    import websocket
except ImportError:
    print("websocket-client is required: pip install websocket-client")
    sys.exit(1)


def create_kernel(base_url: str, token: str) -> str:
    import urllib.request

    url = f"{base_url}/api/kernels"
    headers = {}
    if token:
        headers["X-API-Key"] = token

    req = urllib.request.Request(url, method="POST", data=b"", headers=headers)
    resp = urllib.request.urlopen(req)
    data = json.loads(resp.read())
    kernel_id = data["id"]
    print(f"Created kernel: {kernel_id}")
    return kernel_id


def execute_code(ws: websocket.WebSocket, code: str) -> dict:
    msg_id = str(uuid.uuid4())
    session_id = str(uuid.uuid4())
    msg = {
        "header": {
            "msg_type": "execute_request",
            "msg_id": msg_id,
            "username": "",
            "session": session_id,
            "version": "5.3",
            "date": "",
        },
        "parent_header": {},
        "metadata": {},
        "content": {
            "code": code,
            "silent": False,
            "store_history": True,
            "user_expressions": {},
        },
        "buffers": [],
        "channel": "shell",
    }
    ws.send(json.dumps(msg))

    result = {"stdout": "", "stderr": "", "output": None, "error": None}

    while True:
        resp = json.loads(ws.recv())

        # CRITICAL FIX: Ignore messages left over from previous executions
        parent_id = resp.get("parent_header", {}).get("msg_id")
        if parent_id != msg_id:
            continue

        msg_type = resp.get("msg_type", "")

        if msg_type == "stream":
            result["stdout"] += resp["content"]["text"]
        elif msg_type == "error":
            result["error"] = "\n".join(resp["content"].get("traceback", []))
        elif msg_type == "execute_result":
            result["output"] = resp["content"]["data"]
        elif msg_type == "status":
            if resp["content"]["execution_state"] == "idle":
                break

    return result


def main():
    parser = argparse.ArgumentParser(
        description="Test Jupyter kernel state management in a sandbox"
    )
    parser.add_argument(
        "sandbox_id",
        help="Sandbox ID (e.g. cl-8nxizn9ygtczplsnn9jve38be)",
    )
    parser.add_argument(
        "--domain",
        default="localhost:8080",
        help="Proxy domain (default: localhost:8080)",
    )
    parser.add_argument(
        "--port",
        default="8888",
        help="Jupyter port inside the sandbox (default: 8888)",
    )
    parser.add_argument(
        "--key",
        default="",
        help="Wrenn API Token",
    )
    args = parser.parse_args()

    base_url = f"http://{args.port}-{args.sandbox_id}.{args.domain}"
    ws_base = base_url.replace("http", "ws", 1)

    print(f"Testing Jupyter kernel at {base_url}")
    print()

    kernel_id = create_kernel(base_url, args.key)

    ws_url = f"{ws_base}/api/kernels/{kernel_id}/channels"

    # Pass auth headers to the WebSocket if a token was provided
    ws_headers = {}
    if args.key:
        ws_headers["X-API-Key"] = args.key

    ws = websocket.create_connection(ws_url, header=ws_headers)
    print("Connected to kernel WebSocket")
    print()

    tests = [
        ("variable assignment", "x = 42", None),
        ("read variable", "x * 2", "84"),
        ("import", "import math", None),
        ("use import", "math.sqrt(144)", "12.0"),
        ("function definition", "def greet(name): return f'hello {name}'", None),
        # Fixed: Jupyter 'execute_result' strings include the literal single quotes
        ("call function", "greet('sandbox')", "'hello sandbox'"),
        ("list mutation", "items = [1, 2, 3]; items.append(4); items", "[1, 2, 3, 4]"),
    ]

    passed = 0
    failed = 0

    for name, code, expected in tests:
        print(f"  {name}: {code}")
        result = execute_code(ws, code)

        if result["error"]:
            print(f"    ERROR: {result['error']}")
            failed += 1
            continue

        output = result["stdout"].strip()
        if not output and result["output"]:
            if "text/plain" in result["output"]:
                output = result["output"]["text/plain"].strip()

        if expected is not None:
            if output == expected:
                print(f"    PASS (got: {output})")
                passed += 1
            else:
                print(f"    FAIL (expected: {expected}, got: {output})")
                failed += 1
        else:
            print("    OK")
            passed += 1

    ws.close()
    print()
    print(f"Results: {passed} passed, {failed} failed")
    sys.exit(1 if failed else 0)


if __name__ == "__main__":
    main()
