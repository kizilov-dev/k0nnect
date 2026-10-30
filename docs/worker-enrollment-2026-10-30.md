# Live external Worker enrollment — 2026-10-30

An external VDS was enrolled as **k8nnect-vds** in the supplied
cluster. The node reached Ready with InternalIP **10.203.74.100**, Kubernetes
v1.36.4+k0s, containerd 2.3.4 and Calico 3.32.2 VXLAN. The initial scheduling taint
was removed only after actual workload verification succeeded.

The API-only bootstrap used a short-lived worker token, which was revoked.
Administrator credentials were not installed on the VDS. The new cloned k0s
profile has a separate, named ConfigMap read grant for this node and bootstrap
identity. Existing worker profiles and physical interface addresses were retained.

Three existing workers have Ready WireGuard gateways:

| Worker | Public gateway | Tunnel address |
| --- | --- | --- |
| Existing worker A | Public endpoint | 10.203.74.1 |
| Existing worker B | Public endpoint | 10.203.74.2 |
| Existing worker C | Outbound initiation; no ExternalIP | 10.203.74.3 |

The in-cluster installer Deployment successfully reconciled the connection using
its own ServiceAccount and the supplied SSH credential in Secret vds-ssh.
ConfigMap connection-status reported Ready. Restarting k0sworker and k8nnect-agent
on the VDS completed with both services active and the node Ready.

Initial live verification passed scheduler placement, bidirectional 65536-byte TCP,
UDP payloads 1000/1422/8192 bytes, Service DNS, kubelet logs and WebSocket exec.
The extended verification passed separately against **all three** original workers,
including a worker without ExternalIP, after the worker-service restart.
An error in repeated Service verification was corrected: reusing DNS names after
Service recreation returned cached obsolete ClusterIPs. Each pair now uses fresh
Service names. Direct Pod networking was working during this diagnostic failure.
Failed test resources are retained for diagnosis and removed on the next run.

This validates the tested k0s/Calico configuration. Other CNIs, NetworkPolicy,
cloud-specific load balancers, CSI/storage, sustained load and full host reboot
have not been certified. The VDS has only 1 vCPU and approximately 1 GiB RAM.

Public report: infrastructure endpoints, credentials and raw diagnostic snapshots are omitted.
