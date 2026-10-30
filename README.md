<div align="center">

<img src="website/assets/logo.svg" width="72" height="72" alt="k0nnect">

# k0nnect

**Your server. Your Kubernetes cluster.**

Enroll an external Linux server as a Kubernetes worker over SSH and WireGuard.
A CLI for installation. An addon for ongoing connection management.

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
- Reconciles the connection and reports status when installed as an addon.

Physical server addresses stay in place. Kubelet and containerd use a dedicated
network namespace. The CNI retains ownership of Pod routes and IPAM.

## Requirements

| Component | Supported profile |
| --- | --- |
| Cluster | k0s, Linux/amd64 workers on the same Kubernetes minor version |
| CNI | Validated IPv4 encapsulation profile for Calico, Flannel, Cilium, or kube-router |
| Server | root SSH, systemd, apt, Python 3, kernel WireGuard support |
| Access | kubeconfig with installation, RBAC, and worker-bootstrap permissions |
| Network | Non-overlapping private /24, gateway/server UDP access, package and image access |
| Build | Linux, Go 1.26 or newer; workstation CLI also needs kubectl |

See the [CNI compatibility matrix](https://kizilov-dev.github.io/k0nnect/docs/compatibility/).
Control-plane SSH is not required if the Kubernetes API permits normal worker
bootstrap. The initial installer supports **one external server per cluster**.
Automatic primary-gateway failover is not implemented.

## Quick start

### 1. Build

```sh
git clone https://github.com/kizilov-dev/k0nnect.git
cd k0nnect
make build
make plugin                       # bin/kubectl-k0nnect
export PATH="$PWD/bin:$PATH"
```

### 2. Configure

Copy [examples/connection.json](examples/connection.json) to a private directory
outside the repository. Replace the documentation address and pinned Ed25519 SSH
fingerprint. Use a local `0600` password/key file referenced by `passwordFile`
or `privateKeyFile`; do not put a password inline in JSON.

```sh
install -d -m 700 /secure
cp examples/connection.json /secure/connection.json
chmod 600 /secure/connection.json
```

### 3. Check and preview

```sh
k0nnect connect-check --kubeconfig /secure/cluster.yaml --config /secure/connection.json
k0nnect connect-plan --kubeconfig /secure/cluster.yaml --config /secure/connection.json \
  --output /secure/connect-review.json
```

`connect-check` reads cluster/server state and reports the detected CNI.
`connect-plan` sends only GET to the real API, intercepts writes locally, and does
not use SSH. Secret values are redacted. The plan is for review, not kubectl apply.

### 4. Connect

For persistent management:

```sh
k0nnect addon-install --kubeconfig /secure/cluster.yaml --config /secure/connection.json
kubectl --kubeconfig /secure/cluster.yaml -n k0nnect-system logs -f deployment/installer
kubectl --kubeconfig /secure/cluster.yaml get node k0nnect-vds -o wide
```

For a one-off installation, use `k0nnect connect` with the same arguments.
Commands are also available as `kubectl k0nnect …`. The addon was live-tested on
Calico; the other CNI profiles were tested with the one-off CLI.

## Commands

| Command | Effect |
| --- | --- |
| `connect-check` | Read-only compatibility and pinned-SSH preflight |
| `connect-plan --output PATH` | Local redacted installation plan |
| `connect` | Install, enroll, and verify the server |
| `addon-install` | Install the continuous connection manager |
| `verify-node` | Repeat scheduling, TCP/UDP, DNS, logs, and exec checks |
| `disconnect --drain` | Drain respecting PDBs and remove owned resources |

Commands take `--config PATH --kubeconfig PATH`. Omitting kubeconfig uses an
in-cluster ServiceAccount. The original static kubeadm/Flannel inventory commands
remain documented in the [separate runbook](docs/runbook.md).

## Tested behavior

Fresh enrollment, drain/disconnect, and re-enrollment passed on all four CNI
profiles. Checks covered TCP 64 KiB and UDP 1000/1422/8192 bytes in both directions
against every original worker, Service DNS/HTTP, logs, and exec. The first Calico
lab also tested a worker without ExternalIP, addon reconciliation, and worker-service
restart recovery. See [the full report and build qualifications](docs/cni-matrix-2026-10-30.md).

These are historical tests before the rename. Host reboot, sustained load,
NetworkPolicy, CSI, and cloud LoadBalancer behavior remain uncertified.

## Permissions and credentials

Gateways use hostNetwork and NET_ADMIN. The addon stores SSH credentials in
Secret `vds-ssh` and uses explicit RBAC, including bootstrap and checked serving-CSR
approval. Administrator credentials are not copied to the server. Secret encryption
at rest depends on the cluster. Read [permissions and limits](docs/addon.md).

## Upgrading from the previous name

The rename changes ownership labels, units, directories, and namespaces. Keep the
old binary and configuration and use that version to drain and disconnect first.
Then update your settings and install k0nnect. The new installer refuses detected
legacy managed namespaces to avoid overlapping gateway networks. Existing live
installations are not automatically migrated by a code or repository rename.

## Development

```sh
make check                         # go vet + race tests
make site                          # static output in site/
python3 -m http.server 8080 --directory site
```

Integration scripts in [test/integration](test/integration) modify infrastructure
and require an authorized lab. The English website is built from `website/` and
published by GitHub Actions. Credentials and raw lab snapshots are excluded.
