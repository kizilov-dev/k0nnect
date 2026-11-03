# External worker connector

The connector implements **worker enrollment into one existing cluster**. Joining
independent clusters is a separate, unimplemented mode. WireGuard is the initial
transport; the older transport package has a driver interface, but this new
installer currently uses WireGuard directly. Alternative enrollment transports
still require implementation.

## Supported profiles

Linux/amd64 k0s workers, an accessible Kubernetes API and one of these CNIs,
detected from its DaemonSet:

| CNI | Required configuration |
| --- | --- |
| Calico | IPv4 VXLAN Always / IPIP Never pools, autodetection `interface=eth1` |
| Flannel | `vxlan` backend without DirectRouting, `--kube-subnet-mgr`, `--iface=eth1` or none |
| Cilium | `routing-mode=tunnel` (VXLAN/Geneve), cluster-pool or kubernetes IPAM, `devices` empty or `eth1`, no transparent encryption |
| kube-router | `--run-router`, overlay enabled (`subnet` or `full`), full-mesh BGP |

The installer rejects other configurations and never patches the CNI. Linux VDS with
root SSH, apt, systemd, Python 3, kernel WireGuard and Internet package/release
access is required. Cluster workers pull the k0nnect image from `ghcr.io`. The connector
supports one connection per cluster and one managed worker per VDS; k0s minors
1.30 to 1.40 are accepted.

Installing the controller needs cluster-admin once, to create its CRD and RBAC.
No control-plane SSH is needed when the API permits bootstrap token creation and
normal kubelet bootstrap.

## Network design

On the VDS, kubelet and embedded containerd run in network namespace `k0nnect`.
Its WireGuard interface is named `eth1` so the existing CNI node address selection
(Calico `interface=eth1`, Flannel `--iface=eth1`, Cilium `devices=eth1`, the
kube-router Node InternalIP) chooses the transport address. The device is created in the host namespace
and moved into the worker namespace, retaining its UDP socket in the host network.
The VDS physical interfaces retain their addresses.

Gateway agents run on the existing workers using hostNetwork and NET_ADMIN. They
own interface `k0ngw0`, worker /32 routes with protocol 242 and comment-tagged
iptables forwarding/SNAT rules. The worker namespace default route uses the first
public gateway to reach the private controller, konnectivity and Internet; the
gateway drops forwarded worker traffic to link-local `169.254.0.0/16`, so cloud
metadata of the gateway node stays unreachable from the VDS.
Specific original node IP /32 routes keep CNI tunnel endpoints (VXLAN, Geneve,
IPIP) reachable through the corresponding gateway without masquerading. The CNI
retains ownership of Pod routes and pools.
A gateway without a public address initiates WireGuard to the public VDS and its
endpoint is learned from authenticated packets. This requires outbound UDP and
return traffic; it is not arbitrary NAT traversal. There is no primary gateway
failover yet. The transport MTU is 1400 (gateway `k0ngw0` and the worker default
route). The worker `eth1` instead carries the existing nodes' interface MTU,
published by gateway agents in ConfigMap `gateway-state`: CNIs derive the Pod MTU from
it, and a smaller Pod MTU on the VDS made larger packets arriving through Flannel
or Cilium tunnels vanish at the Pod veth. Larger outer packets are fragmented by
the kernel; the CNI MTU itself is not patched.

UDP NodePort 31820 is used on existing nodes and UDP 31821 on the VDS. Cilium with
kube-proxy replacement rejects bind(2) on NodePort Service ports, so with it the
gateway listens on the port without a Service. The exact
ports and worker subnet are configurable. Keys are generated on their hosts and
stored root-only. An SSH Ed25519 fingerprint is pinned before authentication.

## Controller

`install.yaml` from a release creates namespace `k0nnect-system`, the cluster-scoped
CRD `vdsconnections.k0nnect.io`, ServiceAccount `controller`, its RBAC and a
Deployment. One image, pinned by digest and signed with cosign in the release
workflow, runs the controller, the gateway agents and the gateway cleanup Jobs;
the controller copies its own binary to the VDS over SSH.

```sh
kubectl apply -f https://github.com/kizilov-dev/k0nnect/releases/latest/download/install.yaml
kubectl -n k0nnect-system create secret generic vds-ssh --from-file=private-key=./id_ed25519
kubectl apply -f examples/vdsconnection.yaml   # edited: host, fingerprint, /24
kubectl get vdsconnection
kubectl -n k0nnect-system logs deploy/controller
```

The controller manages one `VDSConnection`, which a CEL rule requires to be
named `vds`; the node is `k0nnect-vds`. These fixed names let the static RBAC be
scoped by name. The controller connects on every spec change and reconciles every
minute; `status.phase` is `Connecting`, `Ready`, `Disconnecting` or `Error`, with
the error in `status.message`. The spec is immutable, so disconnect always reaches
the server the worker was provisioned on. The finalizer `k0nnect.io/disconnect`
is added only after the spec and Secret validate.

Verify the image before installing:

```sh
cosign verify ghcr.io/kizilov-dev/k0nnect@sha256:DIGEST \
  --certificate-identity-regexp '^https://github.com/kizilov-dev/k0nnect/\.github/workflows/release\.yml@refs/tags/v' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

## Credentials and permissions

The controller reads Secret `vds-ssh` (key `private-key` or `password`) through
the API by name; it is not mounted. Kubernetes Secret confidentiality depends on
the cluster's encryption-at-rest and access policy. The administrator kubeconfig
is never copied to the VDS or the controller. Enrollment uses a standard k0s
bootstrap token with a one-hour expiry; the connector revokes it after joining or
a failed attempt. The controller Pod runs as non-root with a read-only root
filesystem and no capabilities.

The controller has static RBAC, not the cluster-admin role:

| Scope | Access |
| --- | --- |
| Named objects | patch/delete Node `k0nnect-vds`; get/delete the bootstrap-token Secret; worker-profile ConfigMaps (one name per supported minor) and Role; gateway ClusterRole; `VDSConnection` `vds` and its status; Secret `vds-ssh` |
| Discovery | get/list Nodes, Namespaces, Services, Calico IPPools, CSRs; get named CNI DaemonSets and ConfigMaps and the default worker profiles |
| Drain | list Pods cluster-wide, create evictions |
| Create | RBAC objects, ConfigMaps and Secrets: Kubernetes cannot restrict create by name |
| CSRs | approve `kubernetes.io/kubelet-serving` after checking identity, signature and SANs |
| Own namespace | Pods, Jobs, DaemonSets, Services, ConfigMaps, ServiceAccounts, Roles |

Gateways run with hostNetwork and NET_ADMIN in a namespace with the privileged
Pod Security level, and the controller holds root SSH to the VDS. Treat it as
node-root-equivalent, like a CNI operator: the RBAC scoping limits mistakes, it
is not a security boundary against a compromised controller.

## CLI

The CLI connects a server once from a workstation and leaves no controller. Use
`k0nnect-linux-amd64` from a release or `make build` (Go >= 1.26); `make plugin`
adds `bin/kubectl-k0nnect`. Copy `examples/connection.json`, supply a root-only
password/key file and replace the documentation endpoint and fingerprint with
independently verified values. Set `"image"` to run gateways from a release image;
otherwise the CLI ships its own binary in compressed ConfigMap chunks into Ubuntu
containers that install the network tools at start.

```sh
bin/k0nnect connect-check --kubeconfig /secure/cluster.yaml --config /secure/connection.json
bin/k0nnect connect-plan --kubeconfig /secure/cluster.yaml --config /secure/connection.json --output /tmp/connect-review.json
bin/k0nnect connect --kubeconfig /secure/cluster.yaml --config /secure/connection.json
bin/k0nnect verify-node --kubeconfig /secure/cluster.yaml --config /secure/connection.json
```

`connect-check` authenticates SSH and reads compatibility/state without mutation.
`connect-plan` sends only GET to the real API and intercepts all proposed writes
locally; it never uses SSH. Its redacted JSON is a review artifact, not an apply
manifest. The CLI keeps its objects in a namespace it owns; the controller refuses
such objects, so disconnect a CLI connection before installing the controller.
The former `addon-install` command is replaced by the controller; disconnect its
installations with `k0nnect disconnect --drain` first.

The worker initially has an initialization NoSchedule taint. Verification schedules
tolerating test workloads and checks TCP 64 KiB and UDP 1000/1422/8192-byte exchanges
in both directions against every Ready existing worker, Service DNS, logs and exec. Only success removes the taint.
Ready alone is not verification. Failed test resources remain for diagnosis and
are cleaned up by the next verification run. Reboot recovery, NetworkPolicy, storage plugins,
cloud-provider behavior and sustained load are not yet certified.

## Removal

```sh
kubectl delete vdsconnection vds
# CLI connections:
bin/k0nnect disconnect --drain --kubeconfig /secure/cluster.yaml --config /secure/connection.json
```

Removal cordons and drains the worker respecting PDBs, stops managed
services/runtime tasks and remaining k0s containerd shims, removes the node's CNI
state on the VDS and removes owned network resources and node. Cleanup validates
ownership and does not reset unrelated hosts. Installed packages, k0s data and
recovery keys remain on the VDS. When the VDS is unreachable but neither the node
nor the gateway network was ever created, only cluster objects are removed. If the
VDS is gone for good after it joined, remove the finalizer by hand and delete the
node and leftovers:

```sh
kubectl patch vdsconnection vds --type merge -p '{"metadata":{"finalizers":null}}'
```

Uninstall the controller after the `VDSConnection` is gone:
`kubectl delete -f install.yaml`.

Live results: [2026-10-30 worker report](worker-enrollment-2026-10-30.md),
[2026-10-30 CNI matrix](cni-matrix-2026-10-30.md).
