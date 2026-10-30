# Connection architecture

The current mode enrolls an external server as a regular worker in **one** Kubernetes cluster. The existing CNI keeps ownership of the Pod network.

## An isolated worker network

The server gets a network namespace named `k0nnect`. Its kubelet and embedded k0s containerd run there. The WireGuard interface inside is named `eth1`: Calico can select it by interface autodetection, Flannel by its interface flag, Cilium by its devices setting, and kube-router uses the Node InternalIP.

Host-network Pods on the external worker, including CNI agents, also use this worker network namespace. Regular Pods have their own Pod namespaces connected by the CNI.

The WireGuard device is created in the host network and then moved into the worker namespace. Its UDP socket stays in the original host network, so encrypted outer traffic uses the physical server connection. Physical interface addresses are retained.

## Gateways on existing workers

A DaemonSet runs gateway agents with hostNetwork and NET_ADMIN. Each owns `k0ngw0`, a /32 route to the external worker, and comment-tagged iptables rules.

The primary gateway provides access from the worker namespace to the private API, konnectivity, and external networks. Forwarded traffic from the worker transport address is masqueraded when it leaves another interface. Specific original node addresses are routed through their corresponding WireGuard peers so CNI-encapsulated packets reach the correct node.

A worker without a public address initiates a connection to the external server. WireGuard learns its endpoint from authenticated traffic. Public gateways are preferred when selecting the primary; there is no automatic failover.

## Ownership boundaries

| Component | Owns |
| --- | --- |
| k0nnect | Transport, gateways, bootstrap, reconciliation, and checks |
| k0s / kubelet | Node enrollment, runtime, and ordinary Pod lifecycle |
| CNI | IPAM, Pod routes, encapsulation, and network policy |
| Kubernetes | API, scheduler, RBAC, bootstrap CSRs, and Services |

`Node.spec.podCIDR` is not a universal source of CNI routes. Discovery checks the actual CNI profile and its address allocation model. See [compatibility](compatibility.md).

## Bootstrap through the API

The installer creates a standard worker bootstrap Secret with a one-hour expiry. Only a bootstrap document containing the trusted CA and short-lived token is copied to the server. The administrator kubeconfig stays on the workstation.

A k0s worker profile is cloned, provider-specific runtime cgroup values are removed, and a dedicated resolver is configured. A named RBAC grant lets the new node and its bootstrap identity read only that profile.

Serving CSRs are checked for requestor identity, signature, CN, organization, and SANs before approval. The token is revoked after enrollment. An initialization taint blocks ordinary scheduling until workload verification passes.

## Continuous management

The addon consists of a Deployment, ConfigMaps, and SSH Secret. Its installer stays on the original workers and reconciles approximately once a minute. On the server, systemd orders network setup before kubelet; a local agent maintains the worker network independently of the management Pod.

The current package ships a static Go binary in compressed ConfigMap chunks and installs runtime tools in Ubuntu containers. A dedicated OCI image is future work.

## Transport boundary

The static profile uses `transport.Driver` to separate reconciliation, status, and removal from transport-specific settings. WireGuard is the first implementation. The automatic installer currently uses WireGuard directly; a different transport needs its own implementation and bootstrap validation.

Interconnecting independent clusters requires a separate mode with explicit prefix and Service exports and trust boundaries.
