"""Bounded synthetic network probes to run INSIDE a disposable tool process.

Only pass the address of a target owned by the test. A failed connect is not
itself proof of no delivery: the outer harness must count target receipts.
"""

from __future__ import annotations

import argparse
import errno
import json
import os
import socket
import subprocess
import sys
import urllib.error
import urllib.request

CHECK_NAMES = (
    "direct_ipv4_tcp", "direct_ipv6_tcp", "direct_ipv4_udp", "direct_ipv6_udp",
    "dns_resolution", "proxy_replacement", "opaque_connect", "shell_child", "grandchild",
)
NETWORK_ERRNOS = {errno.ECONNREFUSED, errno.ENETUNREACH, errno.EHOSTUNREACH,
                  errno.EHOSTDOWN, errno.ETIMEDOUT}
DNS_ERRNOS = {-2, -3, -4, -5}  # EAI_NONAME, EAI_AGAIN, EAI_FAIL, EAI_NODATA
CHILD_BLOCKED_EXIT = 23


class ChildProbeBlocked(Exception):
    def __init__(self, returncode: int):
        super().__init__(returncode)
        self.returncode = returncode


def _error_kind(error: BaseException) -> str:
    if isinstance(error, socket.gaierror):
        return "gaierror"
    if isinstance(error, urllib.error.URLError):
        return "URLError"
    if isinstance(error, TimeoutError):
        return "TimeoutError"
    if isinstance(error, OSError):
        return "OSError"
    return type(error).__name__


def _error_errno(error: BaseException) -> int | None:
    value = getattr(error, "errno", None)
    if value is not None:
        return value
    reason = getattr(error, "reason", None)
    return getattr(reason, "errno", None)


def attempt(name, action):
    try:
        action()
        return {"name": name, "connected_or_sent": True}
    except Exception as error:  # report; the caller decides whether this is expected
        result = {"name": name, "connected_or_sent": False,
                  "error_kind": _error_kind(error), "errno": _error_errno(error)}
        if isinstance(error, ChildProbeBlocked):
            result["returncode"] = error.returncode
        return result


def expected_failure(name: str, result: dict[str, object]) -> bool:
    kind = result.get("error_kind")
    value = result.get("errno")
    if name in {"shell_child", "grandchild"}:
        return kind == "ChildProbeBlocked" and result.get("returncode") == CHILD_BLOCKED_EXIT
    if name == "dns_resolution":
        return kind == "gaierror" and value in DNS_ERRNOS
    if name == "proxy_replacement":
        return kind == "URLError" and value in NETWORK_ERRNOS
    if kind == "TimeoutError":
        return value is None
    return kind == "OSError" and value in NETWORK_ERRNOS


def tcp(address, port, family):
    with socket.socket(family, socket.SOCK_STREAM) as conn:
        conn.settimeout(1)
        conn.connect((address, port))
        conn.sendall(b"GET /probe HTTP/1.1\r\nHost: synthetic.invalid\r\nConnection: close\r\n\r\n")


def connect_tunnel(address, port):
    with socket.create_connection((address, port), timeout=1) as conn:
        conn.sendall(b"CONNECT synthetic.invalid:443 HTTP/1.1\r\nHost: synthetic.invalid:443\r\n\r\n")


def udp(address, port, family):
    with socket.socket(family, socket.SOCK_DGRAM) as conn:
        conn.settimeout(1)
        conn.connect((address, port))
        conn.send(b"synthetic")


def child(address, port, nested=False):
    code = (
        "import errno,socket,sys\n"
        "try:\n s=socket.socket(); s.settimeout(1); "
        " s.connect((sys.argv[1],int(sys.argv[2]))); "
        " s.sendall(b'GET /child HTTP/1.0\\r\\n\\r\\n')\n"
        "except OSError as error:\n"
        " if error.errno in (" + repr(sorted(NETWORK_ERRNOS)) + "):\n"
        "  raise SystemExit(23)\n"
        " raise"
    )
    if nested:
        code = ("import subprocess,sys; result=subprocess.run([sys.executable,'-c'," +
                repr(code) + ",sys.argv[1],sys.argv[2]],check=False); "
                "raise SystemExit(result.returncode)")
    result = subprocess.run([sys.executable, "-c", code, address, str(port)],
                            check=False, timeout=4, capture_output=True)
    if result.returncode == CHILD_BLOCKED_EXIT:
        raise ChildProbeBlocked(result.returncode)
    if result.returncode:
        raise RuntimeError("child_probe_failed")


def shell_child(address, port):
    env = dict(os.environ, SYNTH_IP=address, SYNTH_PORT=str(port))
    command = (
        "python -c 'import errno,os,socket,sys\n"
        "try:\n s=socket.socket(); s.settimeout(1); "
        " s.connect((os.environ[\"SYNTH_IP\"],int(os.environ[\"SYNTH_PORT\"]))); "
        " s.sendall(b\"GET /shell HTTP/1.0\\r\\n\\r\\n\")\n"
        "except OSError as error:\n"
        " if error.errno in (" + repr(sorted(NETWORK_ERRNOS)) + "):\n"
        "  raise SystemExit(23)\n"
        " raise'"
    )
    result = subprocess.run(["/bin/sh", "-c", command], env=env, check=False,
                            timeout=4, capture_output=True)
    if result.returncode == CHILD_BLOCKED_EXIT:
        raise ChildProbeBlocked(result.returncode)
    if result.returncode:
        raise RuntimeError("shell_child_probe_failed")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--ipv4", required=True)
    parser.add_argument("--ipv6", required=True)
    parser.add_argument("--tcp-port", required=True, type=int)
    parser.add_argument("--udp-port", required=True, type=int)
    parser.add_argument("--ipv6-tcp-port", required=True, type=int)
    parser.add_argument("--ipv6-udp-port", required=True, type=int)
    parser.add_argument("--dns-name", required=True)
    parser.add_argument("--expect-success", action="store_true",
                        help="run the same probe code as a reachable positive control")
    args = parser.parse_args()
    os.environ["HTTP_PROXY"] = f"http://{args.ipv4}:{args.tcp_port}"
    os.environ["HTTPS_PROXY"] = os.environ["HTTP_PROXY"]
    os.environ["ALL_PROXY"] = os.environ["HTTP_PROXY"]
    os.environ["NO_PROXY"] = ""
    os.environ["no_proxy"] = ""
    checks = [
        attempt("direct_ipv4_tcp", lambda: tcp(args.ipv4, args.tcp_port, socket.AF_INET)),
        attempt("direct_ipv6_tcp", lambda: tcp(args.ipv6, args.ipv6_tcp_port, socket.AF_INET6)),
        attempt("direct_ipv4_udp", lambda: udp(args.ipv4, args.udp_port, socket.AF_INET)),
        attempt("direct_ipv6_udp", lambda: udp(args.ipv6, args.ipv6_udp_port, socket.AF_INET6)),
        attempt("dns_resolution", lambda: socket.getaddrinfo(args.dns_name, args.tcp_port)),
        attempt("proxy_replacement", lambda: urllib.request.urlopen("http://synthetic.invalid/proxy", timeout=1).read()),
        attempt("opaque_connect", lambda: connect_tunnel(args.ipv4, args.tcp_port)),
        attempt("shell_child", lambda: shell_child(args.ipv4, args.tcp_port)),
        attempt("grandchild", lambda: child(args.ipv4, args.tcp_port, nested=True)),
    ]
    names = [check["name"] for check in checks]
    unexpected_errors = [check for check in checks
                         if not check["connected_or_sent"] and
                         not expected_failure(check["name"], check)]
    unexpected_successes = sum(check["connected_or_sent"] for check in checks)
    result = {"checks": checks, "check_names": names,
              "unexpected_errors": unexpected_errors,
              "unexpected_successes": unexpected_successes}
    if args.expect_success:
        result["positive_control"] = all(check["connected_or_sent"] for check in checks)
        print(json.dumps(result, sort_keys=True))
        return 0 if result["positive_control"] else 1
    result["blocked"] = all(not check["connected_or_sent"] for check in checks)
    print(json.dumps(result, sort_keys=True))
    return 0 if names == list(CHECK_NAMES) and result["blocked"] and not unexpected_errors else 1


if __name__ == "__main__":
    sys.exit(main())
