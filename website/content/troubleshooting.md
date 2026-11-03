# Troubleshooting

Start with `kubectl get vdsconnection`, controller logs, and Pod events. A Node Ready condition alone does not prove end-to-end network connectivity.

```sh
kubectl get vdsconnection vds -o yaml
kubectl -n k0nnect-system logs deployment/controller --tail=100
```

## Phase Error

`Message` holds the last error of the reconciliation; the controller retries about once a minute. Errors about the SSH Secret mean `vds-ssh` is missing or has neither a `private-key` nor a `password` key. Errors before SSH come from discovery: an unsupported CNI, k0s minor, or overlapping transport addresses.

A spec cannot be edited after creation. To fix a wrong value, delete the VDSConnection and create a new one.

## Controller or gateway image cannot be pulled

The controller, gateways, and cleanup Jobs use `ghcr.io/kizilov-dev/k0nnect`. Check `ErrImagePull` events and that workers can reach `ghcr.io`, or mirror the image and render the manifest with `make manifest IMAGE=...`.

## Deletion is stuck

The finalizer stays until the controller has stopped the worker over SSH and removed its cluster objects. Check the `Message` and controller logs; restore SSH access or the `vds-ssh` Secret if they are the cause.

If the server is permanently gone, remove the finalizer and clean leftovers manually: the `k0nnect-vds` Node, the gateway DaemonSet, and the `k0ngw0` interfaces on workers.

```sh
kubectl patch vdsconnection vds --type merge -p '{"metadata":{"finalizers":null}}'
```

## SSH host key mismatch

Check the server address and verify its Ed25519 fingerprint through a trusted channel. Do not replace the pinned key simply to make an error disappear.

## Unsupported CNI configuration

Compare the detected settings with the [compatibility matrix](compatibility.md). k0nnect rejects incompatible modes instead of changing the cluster's CNI configuration. Native routing, host-gw, transparent encryption, and external BGP layouts are not interchangeable with the validated profiles.

## Gateway Pod is not Ready

```sh
kubectl -n k0nnect-system get pods -o wide
kubectl -n k0nnect-system get events
kubectl -n k0nnect-system logs -l app=k0nnect-gateway --tail=100 --prefix
```

Check image access, kernel WireGuard support, NET_ADMIN, and existing IPv4 forwarding. With Cilium kube-proxy replacement, the gateway must not conflict with NodePort bind protection.

## Controller unreachable over the gateway

Check allowed UDP traffic, recent WireGuard handshakes, and gateway access to private API and konnectivity ports. A private-only gateway requires outbound UDP and return traffic. Full NAT traversal is not provided.

## k0s stalls after bootstrap

k0s restricts worker-profile access to specific ConfigMap names. k0nnect creates an additional scoped Role/RoleBinding for its cloned profile. Check that these resources exist and that the profile has not been removed.

Read `journalctl -u k0sworker`. Never attach join tokens or kubelet kubeconfigs to a public issue.

## Node Ready but workload tests fail

```sh
kubectl -n k0nnect-system get jobs,pods -o wide
kubectl -n k0nnect-system logs job/verify-to-external
kubectl -n k0nnect-system logs job/verify-from-external
```

The probe logs direct Pod TCP, Service TCP, and each UDP size. Separate DNS/Service behavior, MTU, and basic transport failures. A missing standard CNI plugin can prevent Pod sandbox creation. kube-router may need a route-sync interval to update masquerade exemptions.

Failed test resources remain available for diagnosis. The next verification cleans them up.

## Legacy installation or ownership conflict

A rename is not an ownership migration. Use the old version to drain and disconnect an old-name installation before installing k0nnect; see [operations](operations.md). k0nnect refuses a detected legacy managed namespace, and the controller refuses objects of a connection made with the CLI or `addon-install`.

Do not overwrite an unrelated Node, namespace, interface, or k0s installation. Use a fresh server or a controlled disconnect for address migration.
