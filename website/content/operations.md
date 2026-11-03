# Operations

Examples assume `kubectl` uses an administrator kubeconfig. CLI commands take `--config` and `--kubeconfig`; they use a private local directory named `/secure`.

## Check connection status

```sh
kubectl get vdsconnection
kubectl get node k0nnect-vds -o wide
kubectl -n k0nnect-system get pods -o wide
```

`Phase` is `Connecting` while a reconciliation runs, `Ready` after it succeeds, `Disconnecting` during removal, and `Error` with the reason in `Message`. The controller reconciles when the object changes and about once a minute, for example to add gateways on new workers.

## Read logs

```sh
kubectl -n k0nnect-system logs deployment/controller --tail=100
kubectl -n k0nnect-system logs -l app=k0nnect-gateway --tail=50 --prefix
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

Repeated reconciliation maintains an owned network. A running Ready worker with a verification annotation is not enrolled again.

## Disconnect a worker

With the controller:

```sh
kubectl delete vdsconnection vds
```

The finalizer `k0nnect.io/disconnect` keeps the object until cleanup finishes. The controller cordons and drains the external node while respecting PodDisruptionBudgets, stops its managed services and runtime shims over SSH, removes node-local CNI state, and removes gateways, RBAC, bootstrap tokens, and worker profiles. If no worker or network was ever started on the server, for example after a wrong address, it removes the cluster objects only.

After a one-off CLI connection:

```sh
k0nnect disconnect --drain \
  --kubeconfig /secure/cluster.yaml \
  --config /secure/connection.json
```

Installed packages, k0s data, and recovery keys remain. Neither path performs a general `k0s reset`. Removal and re-enrollment passed the four-CNI lab matrix with the CLI; production workload behavior still depends on PDBs, storage, and application design.

## Upgrade and uninstall

Upgrade the controller by applying the newer release's `install.yaml`; gateways pick up the new image on the next reconciliation. Automatic Kubernetes upgrades and primary-gateway failover are not implemented.

To uninstall, delete the VDSConnection first and wait until it is gone, then run `kubectl delete -f install.yaml`.

## Move from addon-install or the CLI

The former `addon-install` and `operator` commands are replaced by the controller, and ConfigMap `connection-status` by the VDSConnection status. Disconnect such an installation first with `k0nnect disconnect --drain`, which also stops the old installer Deployment, then install the controller. Do the same for a server connected with `k0nnect connect`: the controller refuses objects owned by another connection.

## Move from the previous project name

The rename changes ownership labels, systemd units, filesystem paths, and network namespace names. It is not an in-place replacement for a running installation under the old name.

Keep the old binary and connection file. First drain and disconnect using that version and confirm the old gateway and worker services are removed. Then install k0nnect as described in the [quick start](getting-started.md).

k0nnect refuses to connect while it detects a legacy managed namespace. This avoids creating two competing gateway networks. The rebranding does not automatically modify your live cluster or server.
