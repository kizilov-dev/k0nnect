# Documentation

**k0nnect turns an external server into a worker in your existing Kubernetes cluster.** Enrollment starts with SSH access and ends with real Pod connectivity checks.

The connector targets k0s on Linux/amd64 with explicitly validated Calico, Flannel, Cilium, and kube-router profiles. It is experimental: passing tests on specific configurations does not certify every CNI release or cloud integration.

## Start here

- [Quick start](getting-started.md) — build the CLI, prepare SSH, and install the addon.
- [Configuration](configuration.md) — connection settings, addresses, and credentials.
- [Compatibility](compatibility.md) — cluster, server, and network requirements.
- [Architecture](architecture.md) — worker isolation, gateways, and traffic paths.

## Two ways to connect

**The addon** runs on the existing workers. It reads configuration and an SSH Secret, reconciles the connection, and reports its status. This is the persistent management option.

**The CLI** connects a server from your workstation with `connect`. A systemd agent maintains the server's network afterward. A one-off connection does not install the management Deployment.

Both use the same bootstrap and verification logic. Without `--kubeconfig`, commands use an in-cluster ServiceAccount.

## What success means

The node must be Ready, real workloads must pass verification, and the `k0nnect.io/initializing` taint must be absent. Checks cover scheduling, bidirectional TCP/UDP against every original Ready worker, Service DNS, logs, and exec.

- [Operations](operations.md) — monitoring, rechecking, removal, and upgrades.
- [Permissions and secrets](security.md) — the addon's access boundaries.
- [Tests and results](verification.md) — the live test matrix and its limits.
- [Troubleshooting](troubleshooting.md) — diagnosing common failures.
