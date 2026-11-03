# Documentation

**k0nnect turns an external server into a worker in your existing Kubernetes cluster.** Enrollment starts with SSH access and ends with real Pod connectivity checks.

The connector targets k0s on Linux/amd64 with explicitly validated Calico, Flannel, Cilium, and kube-router profiles. It is experimental: passing tests on specific configurations does not certify every CNI release or cloud integration.

## Start here

- [Quick start](getting-started.md) — install the controller, add SSH access, and describe the server.
- [Configuration](configuration.md) — connection settings, addresses, and credentials.
- [Compatibility](compatibility.md) — cluster, server, and network requirements.
- [Architecture](architecture.md) — worker isolation, gateways, and traffic paths.

## Two ways to connect

**The controller** runs in the cluster. Install it with `kubectl apply`, then create a `VDSConnection` and an SSH Secret. It reconciles the connection, reports status on the object, and disconnects the server when the object is deleted. Nothing needs to be built locally.

**The CLI** connects a server from your workstation with `connect`. A systemd agent maintains the server's network afterward, but nothing in the cluster reconciles it.

Both use the same bootstrap and verification logic.

## What success means

The node must be Ready, real workloads must pass verification, and the `k0nnect.io/initializing` taint must be absent. Checks cover scheduling, bidirectional TCP/UDP against every original Ready worker, Service DNS, logs, and exec.

- [Operations](operations.md) — monitoring, rechecking, removal, and upgrades.
- [Permissions and secrets](security.md) — the controller's access boundaries.
- [Tests and results](verification.md) — the live test matrix and its limits.
- [Troubleshooting](troubleshooting.md) — diagnosing common failures.
