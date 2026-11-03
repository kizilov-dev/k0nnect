# Tests and results

The connector has been tested against real clusters and external servers. Public reports omit infrastructure endpoints and raw diagnostic snapshots. These are historical results for the connector before rebranding; the rename itself is checked by local tests and CI, not a fresh live enrollment campaign.

## Four-CNI test matrix

On October 30, 2026, four managed k0s 1.36.4 clusters were created at a public cloud provider, each with two workers and one fresh external server. Tests covered `connect`, `disconnect --drain`, and re-enrollment.

| CNI | Tested configuration | Enroll | Disconnect | Re-enroll |
| --- | --- | --- | --- | --- |
| Calico 3.32.2 | VXLAN Always, interface=eth1 | Pass | Pass | Pass |
| Flannel 0.28.9 | VXLAN, iface=eth1 | Pass | Pass | Pass |
| Cilium 1.20.1 | Tunnel/VXLAN, kube-proxy replacement | Pass | Pass | Pass |
| kube-router 2.10.0 | Full-mesh BGP + IPIP overlay | Pass | Pass | Pass |

Pass means scheduling, TCP 64 KiB, UDP 1000/1422/8192 bytes in both directions, direct Pod connectivity, Service DNS/HTTP, logs, and exec against **every** original worker. UDP is checked directly between Pods; the Service check uses HTTP. Original workers stayed Ready.

Calico and Cilium re-enrollment used the final multi-CNI build. Flannel and kube-router used the build immediately before the final Calico-unmount and stale-Ready fixes, then all four connected workers were reconciled with the final build. See the [detailed CNI report](https://github.com/kizilov-dev/k0nnect/blob/main/docs/cni-matrix-2026-10-30.md#result).

## First Calico lab

| Component | Tested configuration |
| --- | --- |
| Kubernetes | k0s 1.36.4 |
| Runtime | containerd 2.3.4 |
| CNI | Calico 3.32.2, IPv4 VXLAN |
| Original workers | Three Ready nodes, including one without ExternalIP |
| External server | Ubuntu 24.04, Linux 6.8, amd64, 1 vCPU, approximately 1 GiB RAM |
| WireGuard MTU | 1400 |

The original lab also checked reconciliation by the former in-cluster installer with its ServiceAccount, kubelet logs/WebSocket exec, and recovery after restarting the installed server services.

## In-cluster controller

The controller was checked on a local single-node k0s 1.36 with kube-router and a containerized stand-in server: discovery, pinned SSH, gateways from the release image, status, finalizer, immutable spec, and both disconnect paths, with and without a reachable server. A full worker enrollment through the controller on a real server is still pending.

## Fixes found through testing

The matrix exposed missing reference CNI plugins, Cilium NodePort bind protection, inconsistent Pod MTUs, kube-router convergence, UDP path-MTU learning, fresh-server package-manager contention, disconnect races, stale CNI state, terminating namespaces, and stale Ready conditions.

Repeated Service tests use unique DNS names to avoid cached ClusterIPs from deleted Services. Failed test resources are kept for diagnosis and cleaned on the next verification run.

## Local checks

```sh
make check
make site
```

Go checks include vet and race-enabled unit tests covering resource ownership, controller RBAC against its own API requests, plan redaction, bootstrap limits, CSR validation, and network/CNI configuration. Site checks validate local links, anchors, language, and branding.

## Integration tests

`test/integration` contains Linux namespace, prepared Kubernetes cluster, and external WireGuard scenarios. These modify infrastructure and require an explicitly authorized lab. The early transport test does not enroll kubelet and cannot establish successful worker enrollment by itself.

## Not certified

Full server reboot, sustained load, throughput/latency budgets, NetworkPolicy, CSI, cloud LoadBalancers, primary-gateway failure, full enrollment through the controller, and other CNI releases or settings. Do not infer those guarantees from Node Ready.
