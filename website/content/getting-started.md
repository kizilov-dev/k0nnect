# Quick start

From SSH access to a verified Kubernetes worker. First check the [compatibility matrix](compatibility.md): the installer only accepts supported profiles.

## 1. Build the CLI

You need Linux, Go 1.26 or newer, and `kubectl`.

```sh
git clone https://github.com/kizilov-dev/k0nnect.git
cd k0nnect
make build
make plugin
export PATH="$PWD/bin:$PATH"
k0nnect connect --help
```

`make plugin` creates `bin/kubectl-k0nnect`. You can also run commands as `kubectl k0nnect`.

## 2. Prepare access

Create a private directory outside the repository. These examples use `/secure`; replace it with your own path.

```sh
install -d -m 700 /secure
cp examples/connection.json /secure/connection.json
chmod 600 /secure/connection.json
```

Set your server's IPv4 address and independently verified Ed25519 SSH fingerprint. Store the password in a separate `0600` file referenced by `passwordFile`. Alternatively, use `privateKeyFile` with an unencrypted SSH private key. Never commit credentials.

The addon requires a password or key file. An SSH agent is supported by the local CLI only and is not forwarded into the cluster.

## 3. Check without making changes

```sh
k0nnect connect-check \
  --kubeconfig /secure/cluster.yaml \
  --config /secure/connection.json
```

This reads the Kubernetes API and server state over pinned SSH. It does not install packages or change the network. The output includes the detected `cni`; incompatible settings are reported as errors.

Generate a review plan:

```sh
k0nnect connect-plan \
  --kubeconfig /secure/cluster.yaml \
  --config /secure/connection.json \
  --output /secure/connect-review.json
```

The plan is generated locally. All writes are intercepted in memory, only GET requests reach the real API, and SSH is not used. Secret values and executable payloads are omitted. This JSON is for review, not for `kubectl apply`.

## 4. Install the addon

```sh
k0nnect addon-install \
  --kubeconfig /secure/cluster.yaml \
  --config /secure/connection.json
```

The addon creates a Deployment, SSH Secret, configuration, and explicit RBAC permissions. During connection it installs gateway agents and enrolls the server. Your administrator kubeconfig is not copied to the server.

```sh
kubectl --kubeconfig /secure/cluster.yaml \
  -n k0nnect-system logs -f deployment/installer
```

Addon operation has been tested on Calico. The additional CNI profiles were tested through the one-off CLI; addon operation on them remains unverified.

## 5. Verify the result

```sh
kubectl --kubeconfig /secure/cluster.yaml get node k0nnect-vds -o wide
kubectl --kubeconfig /secure/cluster.yaml \
  -n k0nnect-system get configmap connection-status -o yaml
```

Wait for Ready and `Worker connected and verified`. The initial scheduling taint is removed only after Pod tests pass. First installation downloads packages and images, so allow several minutes. kube-router convergence can take an additional route-sync interval.

## One-off connection

If you do not need the management Deployment, use this instead of `addon-install`:

```sh
k0nnect connect \
  --kubeconfig /secure/cluster.yaml \
  --config /secure/connection.json
```

This uses the same installation and verification process. Network and worker systemd services remain on the server. See [operations](operations.md) for monitoring and upgrade instructions.
