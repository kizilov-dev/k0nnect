# Connection architecture

The current mode enrolls an external server as a regular worker in **one** Kubernetes cluster. The existing CNI keeps ownership of the Pod network.

## An isolated worker network

The server gets a network namespace named `k0nnect`. Its kubelet and embedded k0s containerd run there. The WireGuard interface inside is named `eth1`: Calico can select it by interface autodetection, Flannel by its interface flag, Cilium by its devices setting, and kube-router uses the Node InternalIP.

Host-network Pods on the external worker, including CNI agents, also use this worker network namespace. Regular Pods have their own Pod namespaces connected by the CNI.

The WireGuard device is created in the host network and then moved into the worker namespace. Its UDP socket stays in the original host network, so encrypted outer traffic uses the physical server connection. Physical interface addresses are retained.

## Gateways on existing workers

A DaemonSet runs gateway agents with hostNetwork and NET_ADMIN. Each owns `k0ngw0`, a /32 route to the external worker, and comment-tagged iptables rules.

The primary gateway provides access from the worker namespace to the private API, konnectivity, and external networks. Forwarded traffic from the worker transport address is masqueraded when it leaves another interface; link-local destinations such as cloud metadata are dropped. Specific original node addresses are routed through their corresponding WireGuard peers so CNI-encapsulated packets reach the correct node.

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

The controller or CLI creates a standard worker bootstrap Secret with a one-hour expiry. Only a bootstrap document containing the trusted CA and short-lived token is copied to the server. No administrator kubeconfig is copied there.

A k0s worker profile is cloned, provider-specific runtime cgroup values are removed, and a dedicated resolver is configured. A named RBAC grant lets the new node and its bootstrap identity read only that profile.

Serving CSRs are checked for requestor identity, signature, CN, organization, and SANs before approval. The token is revoked after enrollment. An initialization taint blocks ordinary scheduling until workload verification passes.

## Continuous management

The install manifest adds CRD `VDSConnection` and a controller Deployment that runs on the original workers. The controller reconciles when the object changes and about once a minute, writes progress to its status, and disconnects the server through a finalizer before the object is deleted. On the server, systemd orders network setup before kubelet; a local agent maintains the worker network independently of the controller.

One image `ghcr.io/kizilov-dev/k0nnect` holds the static Go binary and the network tools. It runs the controller, the gateway agents, and cleanup Jobs; the controller copies its own binary to the server over SSH. The one-off CLI can use the same image or ship its binary in ConfigMap chunks into Ubuntu containers.

## Transport boundary

The static profile uses `transport.Driver` to separate reconciliation, status, and removal from transport-specific settings. WireGuard is the first implementation. The controller and CLI currently use WireGuard directly; a different transport needs its own implementation and bootstrap validation.

Interconnecting independent clusters requires a separate mode with explicit prefix and Service exports and trust boundaries.
