<div align="center">

<img src="website/assets/logo.svg" width="72" height="72" alt="k0nnect">

# k0nnect

**Your server. Your Kubernetes cluster.**

Enroll an external Linux server as a Kubernetes worker over SSH and WireGuard.
A controller you install with `kubectl apply`. A CLI for one-off runs.

[Website](https://kizilov-dev.github.io/k0nnect/) · [Quick start](#quick-start) · [Documentation](https://kizilov-dev.github.io/k0nnect/docs/) · [Architecture](docs/architecture.md) · [Live test matrix](docs/cni-matrix-2026-10-30.md)

</div>

---

k0nnect installs the worker software, connects an external server to your existing
workers, and enrolls kubelet into **the same cluster**. The new node accepts
ordinary workloads after end-to-end checks pass.

The experimental connector has passed live tests with **k0s 1.36.4** and four
CNI profiles: **Calico 3.32.2** (VXLAN), **Flannel 0.28.9** (VXLAN),
**Cilium 1.20.1** (tunnel/VXLAN with kube-proxy replacement), and
**kube-router 2.10.0** (BGP + IPIP overlay). It discovers and validates the CNI;
it does not replace its configuration. Independent-cluster interconnection is a
separate, unimplemented product mode.

## What it does

- Validates k0s/CNI compatibility, address overlap, and the pinned SSH host key.
- Installs k0s worker, WireGuard, and systemd services on the external server.
- Adds missing reference CNI plugins from a SHA256-pinned release.
- Runs gateway agents on the original Ready workers.
- Enrolls the node with a short-lived bootstrap token and revokes it afterward.
- Checks scheduling, bidirectional TCP/UDP against every original worker, Service DNS, logs, and exec.
- Removes the initialization NoSchedule taint only after verification succeeds.
- Reconciles the connection every minute and reports status on a `VDSConnection` object.

Physical server addresses stay in place. Kubelet and containerd use a dedicated
network namespace. The CNI retains ownership of Pod routes and IPAM.

## Requirements

| Component | Supported profile |
| --- | --- |
| Cluster | k0s, Linux/amd64 workers on the same Kubernetes minor version |
| CNI | Validated IPv4 encapsulation profile for Calico, Flannel, Cilium, or kube-router |
| Server | root SSH, systemd, apt, Python 3, kernel WireGuard support |
| Access | kubectl with cluster-admin to apply the install manifest once |
| Network | Non-overlapping private /24, gateway/server UDP access, `ghcr.io` image pulls, package access on the server |

See the [CNI compatibility matrix](https://kizilov-dev.github.io/k0nnect/docs/compatibility/).
Control-plane SSH is not required if the Kubernetes API permits normal worker
bootstrap. The initial installer supports **one external server per cluster**.
Automatic primary-gateway failover is not implemented.

## Quick start

Everything runs in the cluster; nothing is cloned or built on your machine.

### 1. Install the controller

```sh
kubectl apply -f https://github.com/kizilov-dev/k0nnect/releases/latest/download/install.yaml
```

The manifest creates namespace `k0nnect-system`, the `VDSConnection` CRD, the
controller's static RBAC, and its Deployment. The image is pinned by digest and
signed; review the manifest before applying it.

### 2. Give the controller SSH access

```sh
kubectl -n k0nnect-system create secret generic vds-ssh --from-file=private-key=./id_ed25519
```

Use key `password` instead of `private-key` for password login. Root SSH is required.

### 3. Describe the server

Edit [examples/vdsconnection.yaml](examples/vdsconnection.yaml): the server address,
its Ed25519 host-key fingerprint, and a free private /24. Read the fingerprint on the
server itself or in your provider's console, not over the network you are verifying:

```sh
ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub
```

```sh
kubectl apply -f vdsconnection.yaml
kubectl get vdsconnection                 # Phase, Node, Message
kubectl -n k0nnect-system logs -f deploy/controller
kubectl get node k0nnect-vds -o wide
```

The object must be named `vds`; the cluster gets one external server, as node
`k0nnect-vds`. The spec is immutable: delete and recreate the object to change it.

### 4. Disconnect or uninstall

```sh
kubectl delete vdsconnection vds          # drain, stop the worker, remove gateways
kubectl delete -f https://github.com/kizilov-dev/k0nnect/releases/latest/download/install.yaml
```

To upgrade, apply the newer release's `install.yaml`.

### One-off CLI

The CLI connects a server from a workstation without leaving a controller behind.
Download `k0nnect-linux-amd64` from [Releases](https://github.com/kizilov-dev/k0nnect/releases)
(Linux/amd64 only: it copies itself to the server) or run `make build`. Copy
[examples/connection.json](examples/connection.json) to a private `0600` file and
reference a password or key file; never put a password inline.

```sh
k0nnect connect-check --kubeconfig cluster.yaml --config connection.json
k0nnect connect-plan  --kubeconfig cluster.yaml --config connection.json --output review.json
k0nnect connect       --kubeconfig cluster.yaml --config connection.json
k0nnect disconnect --drain --kubeconfig cluster.yaml --config connection.json
```

`connect-check` reads cluster/server state and reports the detected CNI.
`connect-plan` sends only GET to the real API, intercepts writes locally, and does
not use SSH. A cluster connected by the CLI must be disconnected before the
controller takes over; the controller refuses objects it does not own.

| Command | Effect |
| --- | --- |
| `connect-check` | Read-only compatibility and pinned-SSH preflight |
| `connect-plan --output PATH` | Local redacted installation plan |
| `connect` | Install, enroll, and verify the server |
| `verify-node` | Repeat scheduling, TCP/UDP, DNS, logs, and exec checks |
| `disconnect --drain` | Drain respecting PDBs and remove owned resources |
| `manifest --image REF` | Print the controller install manifest for an image |

The original static kubeadm/Flannel inventory commands remain documented in the
[separate runbook](docs/runbook.md).

## Tested behavior

Fresh enrollment, drain/disconnect, and re-enrollment passed on all four CNI
profiles. Checks covered TCP 64 KiB and UDP 1000/1422/8192 bytes in both directions
against every original worker, Service DNS/HTTP, logs, and exec. The first Calico
lab also tested a worker without ExternalIP, reconciliation by the former addon,
and worker-service restart recovery. See [the full report and build qualifications](docs/cni-matrix-2026-10-30.md).

The in-cluster controller was tested on a local single-node k0s 1.36 with a
containerized stand-in server: discovery, SSH, gateways from the image, status,
spec immutability, and both disconnect paths. A full enrollment through the
controller on a real server is still pending. These are historical tests before
the rename. Host reboot, sustained load,
NetworkPolicy, CSI, and cloud LoadBalancer behavior remain uncertified.

## Permissions and credentials

The controller's RBAC is static in `install.yaml`; read it before installing.
Writes outside its namespace are limited by name where Kubernetes allows it: the
node `k0nnect-vds`, one bootstrap-token Secret, the worker profile, and the gateway
role. It also lists Pods cluster-wide and creates evictions to drain the node.
Gateways use hostNetwork and NET_ADMIN, and the controller holds root SSH to the
server, so treat it as node-root-equivalent, like a CNI operator. The SSH credential
stays in Secret `vds-ssh`; administrator credentials are never copied to the
server. Read [permissions and limits](docs/addon.md).

## Upgrading from `addon-install`

The `addon-install` and `operator` commands were replaced by the controller. Run
`k0nnect disconnect --drain` with your old configuration, then install the controller.

## Upgrading from the previous name

The rename changes ownership labels, units, directories, and namespaces. Keep the
old binary and configuration and use that version to drain and disconnect first.
Then update your settings and install k0nnect. The new installer refuses detected
legacy managed namespaces to avoid overlapping gateway networks. Existing live
installations are not automatically migrated by a code or repository rename.

## Development

```sh
make check                         # go vet + race tests
make image                         # local image k0nnect:dev
make manifest IMAGE=k0nnect:dev    # install manifest for that image
make site                          # static output in site/
python3 -m http.server 8080 --directory site
```

Integration scripts in [test/integration](test/integration) modify infrastructure
and require an authorized lab. The English website is built from `website/` and
published by GitHub Actions. Credentials and raw lab snapshots are excluded.
