# Permissions and secrets

k0nnect installs privileged infrastructure. It is an administrator tool, not an isolation boundary between untrusted tenants.

## SSH and keys

The SSH host is checked against a pinned SHA256 fingerprint before remote operations. The tested preflight prefers Ed25519. A mismatch stops the connection.

The controller reads a password or private key from Secret `vds-ssh` in its namespace by name through the API; the Secret is not mounted. The CLI reads a local file or an SSH agent. Credentials are never part of the VDSConnection or the connection JSON.

WireGuard keys are generated on their owning hosts and stored root-only. Peer configuration uses public keys.

## Kubernetes credentials

The controller uses its own ServiceAccount; the CLI keeps your administrator kubeconfig on the workstation. The server receives a worker bootstrap document with a trusted CA and token expiring after one hour. The token is revoked after successful enrollment or a failed attempt; its expiry still applies if cleanup is unavailable.

A Kubernetes Secret does not itself guarantee encryption at rest. etcd protection, encryption settings, and access policy are the cluster administrator's responsibility.

## Gateway privileges

Gateway Pods use hostNetwork, NET_ADMIN, and a dedicated hostPath for state. They create owned interfaces and routes and add tagged forwarding/SNAT rules. Interface aliases, Kubernetes metadata, and the installed-worker marker protect ownership boundaries.

The primary gateway forwards the external worker's traffic to the API, konnectivity, and external networks, masqueraded as the gateway node. It never forwards it to link-local `169.254.0.0/16`, where cloud metadata services expose the gateway node's instance identity and user data. Other private networks the gateway node can reach remain reachable from the external worker, as they are from Pods on existing nodes; restrict them with your firewall if needed.

Gateway agents only read their own Node; they publish their WireGuard public key and interface MTU in the `gateway-state` ConfigMap of their namespace. Neither gateways nor the controller can modify Node objects other than the external worker.

k0nnect does not migrate existing physical addresses or take over CNI Pod routes.

## Controller RBAC

The controller uses explicit roles instead of cluster-admin. Its RBAC is static in `install.yaml`, so you can review it before installation. It needs compatibility reads, owned workload management, gateway RBAC, bootstrap Secret creation, node drain, and checked kubelet-serving CSR approval.

Names are fixed, so writes are scoped by name wherever Kubernetes allows: patch and delete only Node `k0nnect-vds`; get and delete only the bootstrap token Secret with its fixed name; manage only its own k0s worker profiles, their Role, and the gateway ClusterRole; read only the default worker profiles and CNI configuration. Profile names include the k0s minor, so they are listed for 1.30–1.40. It cannot change CoreDNS, kube-proxy, other Nodes, or other roles.

Some permissions stay broad. Kubernetes cannot restrict **create** with `resourceNames`, so RBAC objects, ConfigMaps, and Secrets can be created; RBAC escalation checks still prevent roles broader than the controller's own. Drain needs a cluster-wide Pod list and evictions. Discovery lists Nodes, namespaces, and Services. CSR approval is limited to the kubelet-serving signer.

The controller Pod runs as non-root with a read-only root filesystem and all capabilities dropped. It does, however, create hostNetwork NET_ADMIN Pods in its namespace, which allows privileged Pods, and holds root SSH to the server. Treat it as equivalent to root on the nodes, like a CNI operator; name scoping is defense in depth, not a containment boundary.

Serving CSR checks include the requestor, signer, signature, CN, organization, and IP/DNS SANs. Approving arbitrary certificate requests is not part of the algorithm.

## Image provenance

Release images are signed with keyless cosign in the release workflow, and `install.yaml` pins the image by digest. Verify a digest before installing:

```sh
cosign verify ghcr.io/kizilov-dev/k0nnect@sha256:<digest> \
  --certificate-identity-regexp '^https://github.com/kizilov-dev/k0nnect/\.github/workflows/release\.yml@refs/tags/v' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

Each release also publishes `k0nnect-linux-amd64` and `SHA256SUMS`.

## Operational boundary

Ownership checks and expiring credentials reduce mistakes, but root SSH and NET_ADMIN remain privileged access. Network changes are not a crash-proof distributed transaction. Review the connection plan and recovery procedure before using an important cluster.
