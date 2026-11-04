# E② tool network boundary (B)

This directory implements B's deployment boundary from the **unmerged**
[`e2-network-enforcement.md`](https://github.com/ABC2LAB/ABCDEF2LAB/blob/a5d828a9c1860bee566d38aa932d7149642712fb/docs/contracts/e2-network-enforcement.md)
at `a5d828a9c1860bee566d38aa932d7149642712fb`. It is an integration
template, not a runnable service or an operational approval. The current
`agent.Worker.Execute` and trusted sender still run in one server process.
C must move the tool process across a real process boundary before using this
template. The A package currently has no authenticated Unix-socket service.

## Exit list

| Process | Allowed exit | Enforced by |
| --- | --- | --- |
| Tool and every child | `/run/e2-channel` Unix socket to the trusted A service | Per-run protected external Docker volume mounted read-only; all IP networking disabled with Docker `network_mode: none` |
| Trusted sender | Peers on an operator-created **internal** target network; A must narrow each send to a registered IP and port | B verifies network membership and no host-published ports; A enforces the request |
| Tool | Direct IPv4/IPv6, DNS/UDP, changed proxy, CONNECT and arbitrary Unix sockets | No IP network or other mounted socket is provided; B's runtime check rejects a changed container configuration |
| Either process | Host Docker socket, host network, extra mounts | This template provides none and B's runtime check rejects them |

The sender's registered destination and protocol restrictions are A's
responsibility; Docker's internal network still allows sender traffic to
other peers on that network. C must attach only the intended target and
verify that the sender and approved target are the only two members. The
network must be a non-attachable, local, internal Docker bridge created
with both `com.docker.network.bridge.gateway_mode_ipv4=isolated` and
`com.docker.network.bridge.gateway_mode_ipv6=isolated`. Docker's ordinary
`--internal` bridge still assigns a host bridge address, allowing access to
services on that address. Both isolated gateway modes remove that address;
B rejects a network without them. A target that
cannot join that network needs a separately reviewed host-owned egress design;
do not switch this network to a general bridge. No host ports are published.

The `E2_CHANNEL_VOLUME` must be a fresh C-created external volume for one run, with
only the authenticated A socket inside it. C must verify its ownership,
permissions, protocol, run identity, and lifecycle before starting either
process; this template does not issue a UID, key, epoch, approval, or budget.
The Compose project, both `org.abc2lab.e2.run` labels, and the external
internal network's same label must be supplied from C's already verified
nonsecret run reference. Matching labels are a consistency check, not an
authority or a substitute for C's protected run binding.
The tool receives the directory read-only. Empty proxy variables are only
defense in depth: a tool may replace them, but it still has no IP route.

## Runtime checks and remaining C work

`verify_runtime.py` checks the **actual** Docker objects after C starts them:
tool network mode, mounts and capabilities; sender network and ports; and the
target network's `Internal` flag, both isolated gateway modes, exact two-container membership and channel
volume label. It does not grant authority. C must also
verify that the volume is a plain local volume (no bind-backed `Options`),
inspect its directory contents and enforce the Unix socket ACL; B's metadata
check alone cannot prove the socket's owner or protocol. C must then
provide `BlockNewFlows(run)` and `TerminateAndWait(run)` with cgroup/process
and inherited-descriptor evidence, coordinate A's admission and request
drain, then close audit and release authority in the contract's order.
Stopping a container or A alone is not evidence of that sequence.

`test_verify_runtime.py` exercises the validator with synthetic inspect data.
`test_compose_config.py` renders the Compose template with synthetic values.
`test_compose_docker.py` starts the template with two inert, disposable
processes and verifies their actual Docker state, then removes that project.
`test_boundary_docker.py` checks direct IPv4/IPv6 TCP/UDP, DNS, changed proxy,
opaque CONNECT attempts,
settings, `/bin/sh` child and Python grandchild with positive control and target receipt
counts. `test_channel_docker.py` tests a fixture-only Unix channel and one
HTTP target receipt; it is not A's protected service.
[`ACCEPTANCE.md`](./ACCEPTANCE.md) lists the final A/C cases and the missing
integration inputs.
`probe_network.py` is a bounded probe for a **disposable synthetic target**
inside an isolated tool process. It reports attempts and errors without
collecting payloads or secrets. Its positive registered GET, TLS, revocation,
budget and audit acceptance cases must run against the **real A service**
after C integration. Fixture-only transport checks must not be called NE01,
NE02, NE08, or NE09 acceptance.

No existing `deploy/engine-isolated/`, A `netboundary/`, or C startup file is
modified here. `docker compose -f deploy/e2-network-boundary/compose.yaml`
requires C-provided images, protected channel and internal target network.
Use a unique project per run and remove only that project's disposable
resources after validation.
