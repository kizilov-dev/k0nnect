# Compatibility and limits

k0nnect extends **one existing cluster**. Independent control planes, cross-cluster DNS, and Service export are outside the current product mode.

## Platform requirements

| Component | Requirement |
| --- | --- |
| Kubernetes | k0s 1.30–1.40; original Ready workers use the same minor version |
| Architecture | Linux/amd64 |
| CNI | A validated Calico, Flannel, Cilium, or kube-router profile |
| Server | root SSH, apt, systemd, Python 3, kernel WireGuard |
| API | TLS-verified kubeconfig with client certificate or token |
| Installation | Permission to apply the install manifest: CRD, RBAC, and Deployment |
| Images | Workers can pull `ghcr.io/kizilov-dev/k0nnect` |

## CNI profiles

Discovery identifies the CNI from its DaemonSet and rejects settings that cannot carry encapsulated Pod traffic over the routed transport. The worker's transport interface is named `eth1`, allowing compatible CNI settings to select its transport address without cluster-wide changes.

| CNI | Required profile | Rejected configurations |
| --- | --- | --- |
| Calico | IPv4 IPPool, VXLAN Always, IPIP Never, `IP_AUTODETECTION_METHOD=interface=eth1` | IPIP, unencapsulated BGP, different autodetection |
| Flannel | VXLAN, `--kube-subnet-mgr`, `--iface=eth1` or default route interface | host-gw, DirectRouting, iface-regex, iface-can-reach, public-ip override |
| Cilium | Tunnel mode, VXLAN or Geneve, cluster-pool or Kubernetes IPAM, compatible devices | Native routing, incompatible devices, WireGuard/IPsec transparent encryption |
| kube-router | IPv4 router, overlay enabled as subnet or full, full-mesh BGP | Disabled overlay, external peers, route reflectors |

Live tests used k0s 1.36.4 with Calico 3.32.2, Flannel 0.28.9, Cilium 1.20.1, and kube-router 2.10.0. See [test results](verification.md). These results do not certify every release or configuration.

## CNI-specific behavior

- **Flannel and Cilium:** missing reference CNI plugins are installed from a version-pinned, SHA256-verified release. Existing files are preserved.
- **Cilium:** kube-proxy replacement with NodePort bind protection prevents WireGuard from binding a NodePort Service port. The gateway therefore listens directly without that Service. Reserve the UDP port operationally; do not assign it to a conflicting NodePort Service.
- **kube-router:** masquerade-exemption ipsets converge on the route-sync interval, normally five minutes. Verification retries within that window.
- **kube-router:** an IPIP path can have a lower MTU than the Pods. UDP probes allow up to three transmissions while the sender learns path MTU.
- **Flannel and Cilium:** the worker interface uses the original node network MTU while its default route retains the transport MTU. See [configuration](configuration.md).

## Network requirements

- The transport /24 must not overlap node addresses, Pod pools, or Service IPs.
- Allow gateway UDP `port` on workers and UDP `port + 1` on the external server.
- The worker network must reach the private controller API and konnectivity ports.
- Workers need access to images; the server also needs packages and release files.
- IPv4 forwarding must already be enabled on gateways by the CNI.

A gateway without ExternalIP initiates WireGuard to the public server. This needs outbound UDP and return traffic; arbitrary NAT traversal and relay are not implemented.

## Current limits

- One external server per cluster, and one managed worker per server.
- IPv4 and a statically selected primary gateway; no automatic failover.
- WireGuard is the implemented enrollment transport. Other transports need implementation.
- Existing node address migration requires an explicit disconnect.
- Host reboot, sustained load, NetworkPolicy, CSI, and cloud LoadBalancer behavior remain uncertified.
- One VDSConnection named `vds`; its spec is immutable.
- No Helm chart or management UI yet.

## Why the CNI matters

CNIs may own BGP, eBPF, node address selection, IPAM, policy, and encapsulation. A generic static-route writer cannot assume ownership of those features. k0nnect validates a specific profile and leaves CNI configuration in place.

The static kubeadm/Flannel prototype remains a separate mode. It configures transport but does not automatically enroll a worker.
