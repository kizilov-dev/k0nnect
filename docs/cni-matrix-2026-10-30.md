# External Worker enrollment across CNIs — 2026-10-30

Four managed k0s clusters were created at a public cloud provider, one per available network
driver, each with one controller and two workers in the same availability zone.
Each cluster received its own fresh external VDS in the same location but outside
the cluster's private network. Every VDS was enrolled with `connect`, then removed
with `disconnect --drain` and enrolled again with the final build.

| Component | Version / configuration |
| --- | --- |
| Kubernetes | k0s v1.36.4, containerd 2.3.4 |
| Workers and VDS | Ubuntu 24.04.5, Linux 6.8, amd64; workers 2 vCPU / 2 GiB, VDS 1 vCPU / 2 GiB |
| Calico | 3.32.2, VXLAN Always, IPIP Never, autodetection `interface=eth1` |
| Flannel | 0.28.9, VXLAN, `--kube-subnet-mgr --iface=eth1` |
| Cilium | 1.20.1, tunnel/VXLAN, kube-proxy replacement, `devices=eth1` |
| kube-router | 2.10.0, BGP full mesh with IPIP overlay for other subnets |
| Transport | WireGuard MTU 1400; existing node interface MTU 1500 |

## Result

| CNI | Fresh enrollment | `disconnect --drain` | Re-enrollment |
| --- | --- | --- | --- |
| Calico | PASS after fix 2 | PASS after fix 9 | PASS after fix 11 |
| Flannel | PASS after fixes 1, 3, 5 | PASS after fix 8 | PASS after fixes 9, 10 |
| Cilium | PASS after fixes 1, 4, 5 | PASS after fix 9 | PASS after fix 9 |
| kube-router | PASS after fixes 1, 6, 7 | PASS | PASS after fix 9 |

The numbers refer to the defects below. Calico and Cilium re-enrollment ran on the
final build; Flannel and kube-router on the build before the Calico unmount (part
of fix 9) and fix 11. The final build then reconciled all four connected Workers.

PASS means the full built-in verification: the Node became Ready with InternalIP
10.203.74.100, the scheduler placed workloads on it, and against **each** original
worker TCP 64 KiB and UDP 1000/1422/8192 bytes passed in both directions directly
and through Service DNS, followed by kubelet logs and exec. Only then was the
initialization taint removed. Original workers stayed Ready throughout.

## Defects found and fixed

1. **Calico only.** Discovery required Calico IPPools and failed on every other
   CNI. It now detects the CNI from its DaemonSet and validates a per-CNI profile:
   node address selection by `eth1` and Pod traffic encapsulated between nodes.
2. **apt exit 100 on a fresh VDS.** The provider package mirror intermittently
   refused connections right after boot. Preparation now waits for cloud-init and
   dpkg locks and retries package installation.
3. **Missing CNI plugins (Flannel, Cilium).** These DaemonSets do not install
   `loopback`, `bridge`, `host-local` and `portmap`; managed nodes have them
   preinstalled. Pod sandboxes on the VDS failed until the installer added the
   containernetworking/plugins v1.9.1 release, verified by a pinned sha256,
   without replacing existing files.
4. **Cilium NodePort bind protection.** With kube-proxy replacement Cilium rejects
   `bind(2)` to NodePort Service ports, so gateway WireGuard interfaces failed with
   `Address already in use`. For such Cilium the gateway listens without a Service.
5. **MTU black hole (Flannel, Cilium).** These CNIs derive the Pod MTU from `eth1`.
   With `eth1` at the transport MTU, VDS Pods got 1350/1400 while others had 1450,
   and larger packets from other nodes were dropped silently at the VDS Pod veth:
   UDP above ~1370 bytes and any fragmented UDP never arrived. Gateways now publish
   the node interface MTU; the VDS `eth1` uses it while the Worker default route
   keeps the transport MTU, and oversized WireGuard packets are fragmented.
6. **kube-router convergence.** kube-router adds a new node's Pod subnet to its
   masquerade-exemption ipsets only on its periodic sync (5 minutes). Until then
   replies from VDS Pods were masqueraded to a worker's public address and dropped.
   Verification now retries within the CNI's sync period instead of failing.
7. **PMTUD in the UDP probe (kube-router).** The IPIP tunnel to the VDS has MTU 1380
   while Pods have 1500. The first DF datagram above it is dropped while the sender
   learns the path MTU; the following ones are fragmented. The probe now resends a
   datagram up to three times, as a UDP application would.
8. **Disconnect race.** Killing runtime tasks failed when kubelet deleted a task
   between listing and killing it, leaving the node cordoned but running.
9. **Re-enrollment on the same VDS.** k0s containerd shims outlived `disconnect`
   and kept the old Worker namespace with CNI devices alive, and node-local CNI
   state survived (Flannel `subnet.env` and IPAM leases, Cilium state and BPF pins,
   a Calico cgroup2 mount). The re-registered Node gets a new Pod CIDR, so sandboxes
   failed with `failed to set bridge addr`. `disconnect` now kills the shims and
   removes that state.
10. **Terminating namespace.** `connect` right after `disconnect` hit HTTP 403 while
    the connection namespace was still terminating; it now waits.
11. **Stale Ready.** After an interrupted disconnect `connect` trusted a Node still
    reported Ready although k0sworker was stopped; it now also checks the service.

## Provider observations

The Cilium cluster's agents could not resolve the private API host name for about
ten minutes after creation, so its nodes stayed NotReady; it recovered without
intervention. This is unrelated to k8nnect.

## Not covered

The addon (`addon-install`) was not re-run on the new CNIs: it would store the SSH
credential in a cluster Secret. Its RBAC was extended for the new discovery reads.
NetworkPolicy, VDS reboot, gateway failover, sustained load and other CNI versions
or configurations remain uncertified. Each lab VDS had 1 vCPU and 2 GiB RAM.

Public report: infrastructure endpoints, credentials and raw diagnostic snapshots are omitted.
