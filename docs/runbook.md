# Disposable lab runbook

Do the first deployment on disposable VMs with console access. The current
implementation does not migrate an existing cluster safely or prove its CNI
configuration. Have a local worker and a remote worker for the Pod acceptance
test; include the control-plane node in the transport inventory too.

## Prepare inventory and keys

Install kernel WireGuard, wireguard-tools, iproute2 and iputils ping using your
distribution's package manager. Build the binary with `make build`, then install
it at `/usr/local/bin/k8nnect` on every participating VM.

Generate a distinct key on each node as root; never copy a private key into the
inventory or source repository:

```sh
sudo install -d -m 0700 /etc/k8nnect
sudo sh -c 'umask 077; wg genkey > /etc/k8nnect/node.key'
sudo sh -c 'wg pubkey < /etc/k8nnect/node.key'
```

Copy `examples/inventory.json` and replace the placeholder endpoint addresses,
public keys and node names. Add every participating node. Keep the local
`privateKeyFile` path valid on the corresponding host. The file is opened without
following a symlink and must be a regular, owner-only file owned by the executing
user (root for network commands). The same inventory may reference the same path
on different hosts; the file contents must be unique per node.

Use distinct private transport, Pod and Service CIDRs. Check VPNs, host/container
networks and policy routing as well. The preflight intentionally rejects existing
non-default routes overlapping the transport CIDR, including broad VPN routes.
Endpoints must be literal IPv4 addresses with a reachable UDP port; the listening
port and externally advertised port must match in this first profile. Configure
firewalls explicitly; k8nnect does not open ports or implement relay/rendezvous.

Set `pathMTU` to a conservative known lower bound for the real underlay. For
IPv4 WireGuard, transport MTU must not exceed path MTU minus 60. The example uses
1500/1400; Flannel VXLAN Pod MTU should then be 1350. These values are not a
discovery result or safe defaults for every provider path.

```sh
k8nnect plan --config /etc/k8nnect/inventory.json --node remote-worker
sudo k8nnect doctor --config /etc/k8nnect/inventory.json --node remote-worker
sudo k8nnect apply --config /etc/k8nnect/inventory.json --node remote-worker
```

Repeat with the correct node name on all nodes, then run `verify` on each node.
It checks authenticated transport and transport-sized DF ping packets, not Pod
networking or Kubernetes readiness. ICMP must be allowed for this probe.

## Configure the lab Kubernetes network

Use a kubeadm version accepting `kubeadm.k8s.io/v1beta4`. This prototype does not
pin or install Kubernetes and Flannel releases. Record the actual versions in
the lab report; no release pair has been certified.

For a fresh control plane, configure these kubeadm inputs explicitly:

- API `localAPIEndpoint.advertiseAddress`: the controller's transport address.
- `controlPlaneEndpoint`: the inventory's `kubernetes.apiServer`.
- API certificate SAN: the address used in that endpoint.
- Pod/Service subnets: exactly the inventory values.
- Kubelet `node-ip` on every node: its transport address.

For an existing cluster, review endpoint/certificate, Node IP, Flannel and MTU
migration independently. Do not simply replace Node IPs using this runbook.

Review the Flannel installation manifest for your chosen release. Set
`flanneld --iface=k8n0` (or the inventory interface) on every node and backend
`Type: vxlan`, `DirectRouting: false`. Flannel derives the VXLAN MTU from the
selected interface; verify the effective value in `/run/flannel/subnet.env`
and inside a newly created Pod. Confirm Flannel's node endpoint annotation uses
the transport address. Changing only a net-conf file does not prove the running
daemon selected the intended interface.

You can pass `--flannel-config PATH` to validate a local copy of net-conf.json
against the inventory. The file check rejects an incorrect Pod CIDR, IPv6,
non-VXLAN backend, DirectRouting and non-default VXLAN ports. The agent repeats
this optional check each cycle. Runtime interface selection still needs the
checks above. Confirm IPv4 forwarding is enabled for Pod traffic using the
requirements of your Kubernetes/CNI installation; k8nnect does not change sysctls.

Allow the API, kubelet and Flannel VXLAN traffic on the encrypted interface using
the firewall policy of the cluster. Keep the public underlay restricted to the
intended WireGuard UDP endpoints. The product has no automatic policy management.

## Render and execute a worker join

Obtain a short-lived worker bootstrap token from the cluster administrator and
store it in an owner-only token file on the new worker. Obtain the CA public-key
hash through a trusted channel. Neither credential belongs in the inventory.

```sh
sudo k8nnect join-config \
  --config /etc/k8nnect/inventory.json --node remote-worker \
  --token-file /etc/k8nnect/bootstrap.token \
  --ca-hash sha256:REPLACE_WITH_64_HEX_DIGITS \
  --output /etc/k8nnect/join.json

sudo kubeadm join --config /etc/k8nnect/join.json
```

`join-config` writes JSON (also accepted as YAML by kubeadm), pins the discovery
CA and sets kubelet node-ip. It never creates a control-plane join or executes
kubeadm. The output is created with 0600 permissions and never overwritten.
Remove the token/join files when no longer needed and expire/revoke the token.
See the [upstream kubeadm schema](https://kubernetes.io/docs/reference/config-api/kubeadm-config.v1beta4/).

Check `kubectl get nodes -o wide`, node readiness, Pod placement, logs/exec,
bidirectional Pod TCP/UDP, Service access and DNS. Run
`bash test/integration/kubernetes.sh LOCAL_WORKER REMOTE_WORKER` against this lab.
This creates an isolated namespace and removes it on exit; it does not configure
the cluster or change node settings.

## Start at boot

Install the supplied units and kubelet drop-in under `/etc/systemd/system`.
Write `/etc/k8nnect/node.env` containing `K8NNECT_NODE=remote-worker` (use the
corresponding inventory name on each host). Then reload systemd and enable
`k8nnect-agent.service`. It requires `k8nnect-ready.service`, which applies local
transport before kubelet. The ready unit asserts local configuration only.

```sh
sudo systemctl daemon-reload
sudo systemctl enable --now k8nnect-agent.service
```

Inspect `journalctl -u k8nnect-agent.service` and `k8nnect status`. Agent errors are
retried and logged; service activity does not assert peer or workload readiness.
The agent reloads peer changes every interval. Change inventory files atomically
and distribute revocations to all affected nodes. Identity/address/interface/MTU
migrations require a reviewed agent restart; automatic migration is not supported.

## Recovery and removal

Keep an out-of-band console and the last good inventory. Failed commands trigger
best-effort compensation, but a crash or rollback failure can leave partial state.
Rerun the last good inventory. If an unaliased interface or an orphan blackhole
remains, the preflight refuses ownership: inspect it manually rather than deleting
arbitrary interfaces/routes. There is no durable transaction journal yet.

For removal, first drain workloads through the Kubernetes administrator's normal
process, stop the agent and stop the local kubelet. Then:

```sh
sudo systemctl disable --now k8nnect-agent.service
sudo systemctl stop kubelet.service
sudo k8nnect remove --config /etc/k8nnect/inventory.json --node remote-worker --drained
```

Remove the node's systemd units/drop-in before restarting kubelet or rebooting,
otherwise its dependency will recreate the transport. Kubernetes node cleanup
and `kubeadm reset` remain separate administrator operations.

`remove` deletes the owned interface, its device routes and transport blackhole.
The `--drained` flag acknowledges that removal disrupts the node network; it does
not check drain status or provide a firewall kill switch after removal.
