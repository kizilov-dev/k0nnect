# Quick start

From SSH access to a verified Kubernetes worker. First check the [compatibility matrix](compatibility.md): the controller only accepts supported profiles.

The controller runs in your cluster. Nothing needs to be cloned or built: you need `kubectl`, an administrator kubeconfig, and root SSH access to the server.

## 1. Install the controller

```sh
kubectl apply -f https://github.com/kizilov-dev/k0nnect/releases/latest/download/install.yaml
kubectl -n k0nnect-system rollout status deployment/controller
```

The manifest creates namespace `k0nnect-system`, CRD `vdsconnections.k0nnect.io`, a ServiceAccount, static RBAC, and the controller Deployment. Its image `ghcr.io/kizilov-dev/k0nnect` is pinned by digest. Review the RBAC in the file before applying it; see [permissions](security.md).

Until the first release is published, build the image and manifest from source: `make image IMAGE=registry.example/k0nnect:dev`, push the image, then `make manifest IMAGE=registry.example/k0nnect:dev > install.yaml`.

## 2. Store SSH access

```sh
kubectl -n k0nnect-system create secret generic vds-ssh \
  --from-file=private-key=./id_ed25519
```

The key must be unencrypted. For password access, use the `password` key instead, for example `--from-file=password=./vds-password`. The controller reads this Secret by name through the API; it is not mounted.

## 3. Describe the server

Get the server's Ed25519 host key fingerprint through a trusted channel, for example from the provider console or by running `ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub` on the server.

```yaml
apiVersion: k0nnect.io/v1alpha1
kind: VDSConnection
metadata:
  name: vds
spec:
  host: 203.0.113.10
  hostKeySHA256: SHA256:REPLACE_WITH_VERIFIED_ED25519_FINGERPRINT
  transportCIDR: 10.203.74.0/24
  workerAddress: 10.203.74.100
```

```sh
kubectl apply -f vdsconnection.yaml
```

The object must be named `vds`: the controller manages one server per cluster. The spec is immutable; to change it, delete the object and create a new one. All fields are described in [configuration](configuration.md).

## 4. Watch the connection

```sh
kubectl get vdsconnection
kubectl -n k0nnect-system logs -f deployment/controller
```

`Phase` moves from `Connecting` to `Ready`; `Error` shows the reason in `Message`. First installation downloads packages and images, so allow several minutes. kube-router convergence can take an additional route-sync interval.

```sh
kubectl get node k0nnect-vds -o wide
```

The node accepts ordinary workloads only after Pod tests pass and the `k0nnect.io/initializing` taint is removed.

## One-off connection from a workstation

The CLI connects a server without installing the controller. Download `k0nnect-linux-amd64` from the [releases](https://github.com/kizilov-dev/k0nnect/releases) or build it with `make build` (Linux, Go 1.26 or newer).

```sh
install -d -m 700 /secure
cp examples/connection.json /secure/connection.json
chmod 600 /secure/connection.json
```

Set the address and fingerprint, and reference a `0600` password file with `passwordFile` or a key with `privateKeyFile`. An SSH agent also works for the CLI.

```sh
k0nnect connect-check --kubeconfig /secure/cluster.yaml --config /secure/connection.json
k0nnect connect-plan --kubeconfig /secure/cluster.yaml --config /secure/connection.json \
  --output /secure/connect-review.json
k0nnect connect --kubeconfig /secure/cluster.yaml --config /secure/connection.json
```

`connect-check` makes no changes. `connect-plan` sends only GET requests to the API, does not use SSH, and omits secrets. `connect` runs the same installation and verification as the controller; afterwards systemd services keep the server's network up, but nothing in the cluster reconciles it. See [operations](operations.md).
