# Troubleshooting

Start with `connection-status`, installer logs, and Pod events. A Node Ready condition alone does not prove end-to-end network connectivity.

## SSH host key mismatch

Check the server address and verify its Ed25519 fingerprint through a trusted channel. Do not replace the pinned key simply to make an error disappear.

## Unsupported CNI configuration

Compare the detected settings with the [compatibility matrix](compatibility.md). The installer rejects incompatible modes instead of changing the cluster's CNI configuration. Native routing, host-gw, transparent encryption, and external BGP layouts are not interchangeable with the validated profiles.

## Gateway Pod is not Ready

```sh
kubectl --kubeconfig /secure/cluster.yaml -n k0nnect-system get pods -o wide
kubectl --kubeconfig /secure/cluster.yaml -n k0nnect-system get events
kubectl --kubeconfig /secure/cluster.yaml -n k0nnect-system \
  logs -l app=k0nnect-gateway --tail=100 --prefix
```

Check image and package access, kernel WireGuard support, NET_ADMIN, and existing IPv4 forwarding. With Cilium kube-proxy replacement, the gateway must not conflict with NodePort bind protection.

## Controller unreachable over the gateway

Check allowed UDP traffic, recent WireGuard handshakes, and gateway access to private API and konnectivity ports. A private-only gateway requires outbound UDP and return traffic. Full NAT traversal is not provided.

## k0s stalls after bootstrap

k0s restricts worker-profile access to specific ConfigMap names. The installer creates an additional scoped Role/RoleBinding for its cloned profile. Check that these resources exist and that the profile has not been removed.

Read `journalctl -u k0sworker`. Never attach join tokens or kubelet kubeconfigs to a public issue.

## Node Ready but workload tests fail

```sh
kubectl --kubeconfig /secure/cluster.yaml -n k0nnect-system get jobs,pods -o wide
kubectl --kubeconfig /secure/cluster.yaml -n k0nnect-system logs job/verify-to-external
kubectl --kubeconfig /secure/cluster.yaml -n k0nnect-system logs job/verify-from-external
```

The probe logs direct Pod TCP, Service TCP, and each UDP size. Separate DNS/Service behavior, MTU, and basic transport failures. A missing standard CNI plugin can prevent Pod sandbox creation. kube-router may need a route-sync interval to update masquerade exemptions.

Failed test resources remain available for diagnosis. The next `verify-node` cleans them up.

## Legacy installation or ownership conflict

A rename is not an ownership migration. Use the old version to drain and disconnect an old-name installation before installing k0nnect; see [operations](operations.md). The new installer refuses a detected legacy managed namespace.

Do not overwrite an unrelated Node, namespace, interface, or k0s installation. Use a fresh server or a controlled disconnect for address migration.
