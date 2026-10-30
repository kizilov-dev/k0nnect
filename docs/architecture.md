# Architecture and decisions

Status: experimental. The original static kubeadm/Flannel profile is described
below. The newer API-driven k0s/Calico connector uses a dedicated worker network
namespace and gateway agents; its different routes, firewall ownership and
bootstrap are described in [addon architecture](addon.md). Live enrollment and
workload checks passed for that concrete profile: [report](worker-enrollment-2026-10-30.md).
Production compatibility and operational safety are not certified.

## ADR-001: separate product modes

`cluster-extension` adds workers to a single Kubernetes API and scheduler.
Multi-zone describes that cluster's topology. `cluster-interconnect` would link
independent clusters, retain separate control planes, and introduce explicit
prefix/service exports and authorization. The second mode is rejected by the
current CLI, rather than being treated as another extension topology.

Cross-cluster Pod routing, service export and DNS discovery are separate features.
Overlapping CIDRs require a deliberate NAT/address translation design and are
outside this prototype. There is no implicit trust federation.

## ADR-002: ownership and compatibility

The driver owns one WireGuard interface with alias
`k8nnect:v1:<cluster>:<node>`, a single /32 address and main-table routes using
protocol 242. Reserve protocol 242 for this installation and do not give it a
symbolic mapping in iproute2's rt_protos files. Other protocols, interfaces,
addresses and conflicting routes cause rejection before mutation.

The CNI owns Pod routes, IPAM and service networking. k8nnect never writes those
routes, existing firewall rules, CNI CRDs or default routes. The prototype uses
WireGuard to provide node IP connectivity underneath Flannel VXLAN. All nodes,
including controllers, need participating transport addresses and a reviewed CNI
configuration. Moving existing Node IPs is an explicit migration, not onboarding.

Supported declaration: Linux, private non-overlapping IPv4 networks, kubeadm,
Flannel VXLAN with DirectRouting disabled. This is a candidate compatibility
profile, not certification of every release. The lab must record concrete kernel,
Kubernetes, Flannel and iproute2 versions before a profile can be certified.
Calico, Kube-router, Cilium, Flannel host-gw and dual stack are rejected.

The network adapter for a future profile must determine route ownership, IPAM
sources, node address selection, encapsulation, encryption, policy behavior and
MTU. Do not assume Node.spec.podCIDR is authoritative for every IPAM.

## ADR-003: transport boundary

`transport.Driver` exposes capabilities, reconciliation, status and removal.
`transport.Link` carries endpoints and reachable prefixes, without WireGuard keys.
The inventory's driver-specific parameters live in each node's `wireguard` block.
Only WireGuard is implemented; another transport needs its own validated schema,
driver factory branch and capability checks. There is no claim of drop-in
equivalence between transports with different security or routing semantics.

The MVP uses a full mesh: each peer receives only the other node's /32 in
AllowedIPs. No Pod or Service prefixes, default route, or arbitrary subnet exports
are granted to peers. Endpoint IPv4 addresses must be outside all internal CIDRs
and have an independent underlay route. ListenPort is the port in the local node's
endpoint, so NAT port remapping is outside this initial profile.

A lower-priority blackhole route for the transport CIDR prevents unknown node
addresses falling through to a default route while the managed network exists.
This is not a complete firewall or a guarantee against another privileged process
changing routes. Removal also deletes the guard and therefore requires draining.

## ADR-004: small deployment and explicit bootstrap

One Go binary provides CLI and systemd agent. Static inventory avoids a new
management service and database. Keys are generated locally with existing tools;
bootstrap tokens are separate from transport identity. Future enrollment must
bind an authenticated node to authorized addresses and public keys.

The agent starts outside Kubernetes so a disconnected worker can bootstrap.
A oneshot unit orders local transport configuration before kubelet; this does not
assert remote peer readiness. The agent continues reconciliation without a
central server. Every inventory update must be distributed to affected nodes;
revocation is effective only after those nodes reconcile. There is no central
instant revocation or automatic key rotation yet.

For static-profile inspect/apply, the driver uses bounded `ip`/`wg` subprocesses without
a shell. Enrollment additionally uses Go SSH and WebSocket modules. The
runner boundary permits replacement with netlink and wgctrl after integration
tests exist. `wg` input and potentially secret-bearing errors are never logged;
status never uses `wg dump`, which contains private keys.

Successful changes are compensated in reverse order after a failed command,
including restoring prior WireGuard configuration. This is not a cross-command
kernel transaction. Unexpected crashes or another privileged network writer can
leave partial state. Keep the last known good inventory and host console access.

## Reuse investigation

- [Kilo](https://kilo.squat.ai/docs/introduction/) is the nearest existing project
  for one distributed cluster; its documented add-on support includes Flannel.
  This prototype is not a Kilo integration or a replacement proven superior to it.
- [Submariner](https://submariner.io/getting-started/architecture/) is the first
  candidate for the separate multi-cluster mode: gateway routing, service
  discovery and optional Globalnet already exist.
- [Cilium Cluster Mesh](https://docs.cilium.io/en/stable/network/clustermesh/setup/)
  is a candidate for Cilium-only deployments, with its own compatibility and
  address requirements.
- WireGuard limitations and keepalive semantics are documented in the
  [upstream limitations](https://www.wireguard.com/known-limitations/) and
  [quick start](https://www.wireguard.com/quickstart/).
- [Flannel backends](https://github.com/flannel-io/flannel/blob/master/Documentation/backends.md)
  specify the host-gw L2 requirement and VXLAN behavior.
- [Calico routing](https://docs.tigera.io/calico/latest/networking/configuring/vxlan-ipip),
  [Kube-router BGP](https://www.kube-router.io/docs/bgp/) and
  [Cilium routing](https://docs.cilium.io/en/stable/network/concepts/routing/)
  demonstrate why adapters cannot be generic static route writers.

Kilo/Submariner have been reviewed through documentation, not benchmarked on a
live stand. Complete that comparison before committing to a production dataplane.

## Next milestones and acceptance gates

1. Run the namespace integration test on an isolated Linux VM. Capture versions,
   encrypted packet flow, rollback behavior and repeated apply results.
2. Prepare a fresh two-zone kubeadm/Flannel lab. Run the Kubernetes test and
   explicitly verify Node IPs, Flannel endpoint annotations and Pod MTU.
3. Test UDP, large packets/PMTU, peer removal, agent restart, node reboot and
   network partitions. Observe scheduling and application behavior during loss
   of the API link; do not equate network reconnection with workload availability.
4. Compare throughput, latency and CPU against direct WireGuard on the same VM
   pair. Set performance budgets from measured baselines.
5. Evaluate Kilo against this same test matrix and document reuse decisions.
6. Add k0s + Kube-router as a separate profile with real BGP/next-hop integration.
7. Design authenticated enrollment and management APIs only after profile one
   passes its acceptance gates. Investigate Submariner for multi-cluster separately.
