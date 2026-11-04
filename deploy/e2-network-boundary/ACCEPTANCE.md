# B and A/C acceptance handoff

Baseline: `dev` `f21d73fe89315ae69bd70051fb2d043d363ea1d0`, open contract
[#183](https://github.com/ABC2LAB/ABCDEF2LAB/pull/183) head
`a5d828a9c1860bee566d38aa932d7149642712fb`, open A
[#184](https://github.com/ABC2LAB/ABCDEF2LAB/pull/184) head
`a17b86f4eb82cfcad80d63f568b9b21e23370e7d`. Recheck all three before
integration. #184 exposes Go `Open`, `SendRegisteredRead`, `CloseAdmission`,
`StopAndWait`; it does not expose an authenticated Unix socket listener.

The B-only tests exercise disposable synthetic containers and must be kept
separate from actual A+B+C acceptance. No real account, UID, approval, budget,
audit key/epoch, model, or target is issued here.

| Case | Current preparation | Final evidence required with actual A/C |
| --- | --- | --- |
| NE01 HTTP | Fixture channel forwards one fixed GET; target receives one. | C's protected socket must call actual A `SendRegisteredRead`; exact registered HTTP GET receives one, result ACK observed, changed proxy env does not alter route. |
| NE02 HTTPS | Unexecuted. | Actual A uses a disposable synthetic HTTPS target with a test CA, pinned IP and correct name; exact GET receives one and A reports the verified response. No insecure TLS option. |
| NE06 direct exits | Disposable dual-stack target positive control and `network none` probe cover IPv4/IPv6 TCP+UDP, DNS, changed proxy, `/bin/sh` child and Python grandchild; new target receipts must remain zero. | Run these probes inside **C's real tool image** and Compose process, including the intended Unix socket, arbitrary localhost/Unix socket paths, inherited descriptors and a fallback attempt after each rejection. Count target receipts independently. |
| NE07 alternate tools | Unexecuted. | Start actual Bash, WebFetch, Search, MCP, browser and their children through C's tool process; for each either show disabled/absent or attempt a synthetic target and count zero receipts. No general tool gets A's protected socket authority by copying an environment variable. |
| NE08 TLS and CONNECT | B-only direct CONNECT attempt to a synthetic responder is blocked with zero new receipts; actual A TLS and MITM paths are unexecuted. | With actual A service, wrong-name, expired, untrusted and failed-handshake TLS each produce zero target application GETs and no direct fallback. CONNECT and MITM passthrough attempts from the real tool process deliver zero, regardless of traffic capture toggle. |
| NE04/05/09 stop, revoke, budget, audit | Unexecuted. | Use C-provided synthetic current authority, shared ledger and audited A sender. Race fresh reads, new direct flows and child creation against revocation/expiry, budget exhaustion, A `CloseAdmission/StopAndWait`, B `BlockNewFlows/TerminateAndWait`, audit close and key release. Report admission, reservations, HTTP receipts, partial/unknown sends, audit ACKs and remaining processes/sockets separately. A drain alone is insufficient; unknown drain keeps isolation and forbids success Seal. |
| NE10 secret handling | Unexecuted. | Inject synthetic sentinel strings in errors/responses, then count exact sentinel occurrences in bounded model/receipt/audit/shared log outputs without publishing those outputs. Expected zero and no success promotion after audit failure. |

Before any final case, C must supply and verify one run's actual host-owned
process identity, protected channel volume and Unix socket ACL, exact target
container/network, registered resource mapping, authority/ledger/audit
adapters, and process/cgroup termination evidence. The current Worker runs in
the server process, so this Compose template cannot activate the general
tool boundary by itself. Any missing real source is a blocked or unexecuted
case, never a passing synthetic substitute.
