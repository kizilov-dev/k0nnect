# Permissions and secrets

k0nnect installs privileged infrastructure. It is an administrator tool, not an isolation boundary between untrusted tenants.

## SSH and keys

The SSH host is checked against a pinned SHA256 fingerprint before remote operations. The tested preflight prefers Ed25519. A mismatch stops the connection.

The CLI reads a password or private key from a local file. The addon copies it into Secret `vds-ssh`, mounted with mode `0400` in the installer Pod. Credentials are not included inline in the public connection JSON.

WireGuard keys are generated on their owning hosts and stored root-only. Peer configuration uses public keys.

## Kubernetes credentials

Your administrator kubeconfig stays on the workstation. The server receives a worker bootstrap document with a trusted CA and token expiring after one hour. The token is revoked after successful enrollment or a failed attempt; its expiry still applies if cleanup is unavailable.

A Kubernetes Secret does not itself guarantee encryption at rest. etcd protection, encryption settings, and access policy are the cluster administrator's responsibility.

## Gateway privileges

Gateway Pods use hostNetwork, NET_ADMIN, and a dedicated hostPath for state. They create owned interfaces and routes and add tagged forwarding/SNAT rules. Interface aliases, Kubernetes metadata, and the installed-worker marker protect ownership boundaries.

The installer does not migrate existing physical addresses or take over CNI Pod routes.

## Installer RBAC

The addon uses explicit roles instead of cluster-admin. It needs compatibility reads, Node patches, owned workload management, gateway RBAC, bootstrap Secret creation, and checked kubelet-serving CSR approval.

Bootstrap Secret read/delete is scoped to the connection's deterministic token name. Kubernetes cannot restrict Secret **create** with `resourceNames`, so create permission in kube-system remains broader.

Serving CSR checks include the requestor, signer, signature, CN, organization, and IP/DNS SANs. Approving arbitrary certificate requests is not part of the algorithm.

## Operational boundary

Ownership checks and expiring credentials reduce mistakes, but root SSH and NET_ADMIN remain privileged access. Network changes are not a crash-proof distributed transaction. Review the connection plan and recovery procedure before using an important cluster.
