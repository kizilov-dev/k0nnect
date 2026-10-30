# External worker connector

The connector implements **worker enrollment into one existing cluster**. Joining
independent clusters is a separate, unimplemented mode. WireGuard is the initial
transport; the older transport package has a driver interface, but this new
installer currently uses WireGuard directly. Alternative enrollment transports
still require implementation.

## Supported initial profile

Linux/amd64 k0s workers, an accessible Kubernetes API, Calico IPv4 VXLAN Always /
IPIP Never, and Calico node address autodetection `interface=eth1`. The installer
rejects other profiles. This is not a claim of CNI independence. Linux VDS with
root SSH, apt, systemd, Python 3, kernel WireGuard and Internet package/release
access is required. Cluster workers also need package/image access. The initial
implementation supports one connection per cluster and one managed worker per VDS.

The caller needs permissions to install the listed resources; a kubeconfig with
only workload access cannot install this addon. No control-plane SSH is needed
when the API permits bootstrap token creation and normal kubelet bootstrap.

## Network design

On the VDS, kubelet and embedded containerd run in network namespace `k8nnect`.
Its WireGuard interface is named `eth1` so this specific Calico autodetection
profile chooses the transport address. The device is created in the host namespace
and moved into the worker namespace, retaining its UDP socket in the host network.
The VDS physical interfaces retain their addresses.

Gateway agents run on the existing workers using hostNetwork and NET_ADMIN. They
own interface `k8ngw0`, worker /32 routes with protocol 242 and comment-tagged
iptables forwarding/SNAT rules. The worker namespace default route uses the first
public gateway to reach the private controller, konnectivity and Internet.
Specific original node IP /32 routes keep Calico VXLAN endpoints reachable through
the corresponding gateway. Calico retains ownership of Pod routes and pools.
A gateway without a public address initiates WireGuard to the public VDS and its
endpoint is learned from authenticated packets. This requires outbound UDP and
return traffic; it is not arbitrary NAT traversal. There is no primary gateway
failover yet. The transport MTU is 1400; existing Calico MTU is not patched, so
fragmentation of its VXLAN packets must be verified on the actual installation.

UDP NodePort 31820 is used on existing nodes and UDP 31821 on the VDS. The exact
ports and worker subnet are configurable. Keys are generated on their hosts and
stored root-only. An SSH Ed25519 fingerprint is pinned before authentication.

## Credentials and permissions

The addon copies the provided SSH password/key into Secret `vds-ssh`, mounted
0400 in its installer Pod. Kubernetes Secret confidentiality depends on the
cluster's encryption-at-rest and access policy. The administrator kubeconfig is
never copied to the VDS or addon. Enrollment uses a standard k0s bootstrap token
with a one-hour expiry; the connector revokes it after joining or a failed attempt.

The installer has explicit RBAC, not the cluster-admin role. It can patch Nodes,
approve kubelet-serving CSRs and manage its gateway resources. CSR identity,
signature and SANs are checked before approval. Bootstrap Secret read/delete is
limited to the connection's deterministic token name. Kubernetes cannot restrict
Secret **create** by resourceNames, so that permission in kube-system remains
broader. Installation itself requires an administrator able to grant these roles.
This is privileged infrastructure software and is not a namespace security boundary.

## CLI and addon

Build a static binary with Go >= 1.26 (`make build`). `make plugin` additionally
creates `bin/kubectl-k8nnect`; add this directory to PATH to use `kubectl k8nnect`.
Copy `examples/connection.json`, supply a root-only password/key file and replace
the documentation endpoint and fingerprint with independently verified values.

```sh
bin/k8nnect connect-check --kubeconfig /secure/cluster.yaml --config /secure/connection.json
bin/k8nnect connect-plan --kubeconfig /secure/cluster.yaml --config /secure/connection.json --output /tmp/connect-review.json
# Install and run continuously in the cluster:
bin/k8nnect addon-install --kubeconfig /secure/cluster.yaml --config /secure/connection.json
# Or connect once from the workstation:
bin/k8nnect connect --kubeconfig /secure/cluster.yaml --config /secure/connection.json
```

`connect-check` authenticates SSH and reads compatibility/state without mutation.
`connect-plan` sends only GET to the real API and intercepts all proposed writes
locally; it never uses SSH. Its redacted JSON is a review artifact, not an apply
manifest. The addon uses a Deployment, ConfigMaps and Secret rather than a CRD.
The initial packaging ships the executable in compressed ConfigMap chunks and
installs runtime tools in Ubuntu containers; a dedicated image is future work.

```sh
kubectl -n k8nnect-system get configmap connection-status -o yaml
kubectl -n k8nnect-system logs deployment/installer
kubectl get node k8nnect-vds -o wide
bin/k8nnect verify-node --kubeconfig /secure/cluster.yaml --config /secure/connection.json
```

The worker initially has an initialization NoSchedule taint. Verification schedules
tolerating test workloads and checks TCP 64 KiB and UDP 1000/1422/8192-byte exchanges
in both directions against every Ready existing worker, Service DNS, logs and exec. Only success removes the taint.
Ready alone is not verification. Failed test resources remain for diagnosis and
are cleaned up by the next verification run. Reboot recovery, NetworkPolicy, storage plugins,
cloud-provider behavior and sustained load are not yet certified.

## Removal

```sh
bin/k8nnect disconnect --drain --kubeconfig /secure/cluster.yaml --config /secure/connection.json
```

Removal scales down the installer, cordons and drains the worker respecting PDBs,
stops managed services/runtime tasks and removes owned network resources and node.
Cleanup validates ownership and does not reset unrelated hosts. Installed packages,
k0s data and recovery keys remain on the VDS. Live removal still needs validation.

Live enrollment results: [2026-10-30 worker report](worker-enrollment-2026-10-30.md).
