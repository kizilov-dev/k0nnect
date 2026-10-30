# Operations

Commands take `--config` and `--kubeconfig`. Examples below use a private local directory named `/secure`.

## Check connection status

```sh
kubectl --kubeconfig /secure/cluster.yaml get node k0nnect-vds -o wide
kubectl --kubeconfig /secure/cluster.yaml -n k0nnect-system get pods -o wide
kubectl --kubeconfig /secure/cluster.yaml -n k0nnect-system \
  get configmap connection-status -o yaml
```

The status ConfigMap contains `phase`, node name, timestamp, and details. `Connecting` means a reconciliation cycle is running; `Ready` means it completed successfully; `Error` needs diagnosis.

## Read logs

```sh
kubectl --kubeconfig /secure/cluster.yaml -n k0nnect-system \
  logs deployment/installer --tail=100
kubectl --kubeconfig /secure/cluster.yaml -n k0nnect-system \
  logs -l app=k0nnect-gateway --tail=50 --prefix
```

On the server:

```sh
systemctl status k0nnect-network k0nnect-agent k0sworker
journalctl -u k0sworker -u k0nnect-agent --since '10 minutes ago'
```

Do not publish bootstrap files, kubeconfigs, private keys, or raw configuration snapshots. Ordinary `wg show` hides the private key; dump output may expose it.

## Repeat workload verification

```sh
k0nnect verify-node \
  --kubeconfig /secure/cluster.yaml \
  --config /secure/connection.json
```

This creates owned temporary Pods, Services, and Jobs and checks the external node against every original Ready worker. Successful tests clean up their resources. Failed resources remain for diagnosis and are removed on the next verification run.

Repeated `connect` or addon reconciliation maintains an owned network. A running Ready worker with a verification annotation is not enrolled again.

## Disconnect a worker

```sh
k0nnect disconnect --drain \
  --kubeconfig /secure/cluster.yaml \
  --config /secure/connection.json
```

The command stops the installer, cordons and drains the external node while respecting PodDisruptionBudgets, stops its managed services and runtime shims, removes node-local CNI state, and removes owned network and cluster resources.

Installed packages, k0s data, and recovery keys remain. It does not perform a general `k0s reset`. Removal and re-enrollment passed the four-CNI lab matrix; production workload behavior still depends on PDBs, storage, and application design.

## Upgrade an existing k0nnect installation

Build the new release, then repeat `addon-install` to update the installer. Changing the transport address of an enrolled worker requires an explicit disconnect. Automatic Kubernetes upgrades and primary-gateway failover are not implemented.

## Move from the previous project name

The rename changes ownership labels, systemd units, filesystem paths, and network namespace names. It is not an in-place replacement for a running installation under the old name.

Keep the old binary and connection file. First drain and disconnect using that version. Confirm the old gateway and worker services are removed. Then update the node name and namespace in your connection file to the new examples and run `k0nnect connect-check` followed by `connect` or `addon-install`.

The new installer refuses to connect while it detects a legacy managed namespace. This avoids creating two competing gateway networks. The rebranding does not automatically modify your live cluster or server.
