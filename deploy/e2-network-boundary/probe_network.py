"""Bounded synthetic network probes to run INSIDE a disposable tool process.

Only pass the address of a target owned by the test. A failed connect is not
itself proof of no delivery: the outer harness must count target receipts.
"""

from __future__ import annotations

import argparse
import json
import os
import socket
import subprocess
import sys
import urllib.request


def attempt(name, action):
    try:
        action()
        return {"name": name, "connected_or_sent": True}
    except (OSError, ValueError, subprocess.CalledProcessError, subprocess.TimeoutExpired) as error:
        return {"name": name, "connected_or_sent": False, "errno": getattr(error, "errno", None)}


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
        "import socket,sys; s=socket.socket(); s.settimeout(1); "
        "s.connect((sys.argv[1],int(sys.argv[2]))); s.sendall(b'GET /child HTTP/1.0\\r\\n\\r\\n')"
    )
    if nested:
        code = "import subprocess,sys; subprocess.run([sys.executable,'-c'," + repr(code) + ",sys.argv[1],sys.argv[2]],check=True)"
    subprocess.run([sys.executable, "-c", code, address, str(port)], check=True, timeout=4, capture_output=True)


def shell_child(address, port):
    env = dict(os.environ, SYNTH_IP=address, SYNTH_PORT=str(port))
    command = (
        "python -c 'import os,socket; s=socket.socket(); s.settimeout(1); "
        "s.connect((os.environ[\"SYNTH_IP\"],int(os.environ[\"SYNTH_PORT\"]))); "
        "s.sendall(b\"GET /shell HTTP/1.0\\r\\n\\r\\n\")'"
    )
    subprocess.run(["/bin/sh", "-c", command], env=env, check=True, timeout=4, capture_output=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--ipv4", required=True)
    parser.add_argument("--ipv6", required=True)
    parser.add_argument("--tcp-port", required=True, type=int)
    parser.add_argument("--udp-port", required=True, type=int)
    parser.add_argument("--ipv6-tcp-port", required=True, type=int)
    parser.add_argument("--ipv6-udp-port", required=True, type=int)
    parser.add_argument("--dns-name", required=True)
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
    print(json.dumps({"checks": checks, "unexpected_successes": sum(c["connected_or_sent"] for c in checks)}))
    return 1 if any(c["connected_or_sent"] for c in checks) else 0


if __name__ == "__main__":
    sys.exit(main())
