# Configuration

The controller reads a `VDSConnection` object and Secret `vds-ssh`. The CLI reads a connection JSON file with the same network settings.

## VDSConnection

The address and fingerprint below are placeholders. Replace them with your server's actual address and verified SSH fingerprint.

```yaml
apiVersion: k0nnect.io/v1alpha1
kind: VDSConnection
metadata:
  name: vds
spec:
  host: 203.0.113.10
  sshPort: 22
  hostKeySHA256: SHA256:REPLACE_WITH_VERIFIED_ED25519_FINGERPRINT
  transportCIDR: 10.203.74.0/24
  workerAddress: 10.203.74.100
  port: 31820
  mtu: 1400
```

The object is cluster-scoped and must be named `vds`. Names are fixed so the install manifest can scope RBAC by name: the node is `k0nnect-vds`, the namespace `k0nnect-system`. The spec is immutable, so a disconnect always reaches the server the worker was installed on.

## Secret vds-ssh

| Key | Meaning |
| --- | --- |
| `private-key` | SSH private key without a passphrase |
| `password` | SSH password, as an alternative |

The Secret lives in `k0nnect-system`. The SSH user is `root`.

## Addresses and ports

| Field | Constraint |
| --- | --- |
| `host` | Literal server IPv4 address; hostnames are not supported |
| `sshPort` | SSH port; defaults to 22 |
| `hostKeySHA256` | Required pinned host fingerprint; the tested preflight uses Ed25519 |
| `transportCIDR` | Non-overlapping, canonical private IPv4 /24 |
| `workerAddress` | Inside transportCIDR, with last octet 100–254 |
| `port` | Gateway UDP port in 30000–32766; defaults to 31820 |
| `mtu` | WireGuard transport MTU, 1280–1440; defaults to 1400 |
| `k0sVersion` | Optional k0s release; normally discovered from the worker profile |

The server listens on `port + 1`. Initial /24 addresses are assigned to gateways, leaving `.100` onward for the worker.

## CLI connection file

```json
{
  "name": "external-worker",
  "nodeName": "k0nnect-vds",
  "namespace": "k0nnect-system",
  "host": "203.0.113.10",
  "user": "root",
  "sshPort": 22,
  "passwordFile": "/secure/vds-password",
  "hostKeySHA256": "SHA256:REPLACE_WITH_VERIFIED_ED25519_FINGERPRINT",
  "transportCIDR": "10.203.74.0/24",
  "workerAddress": "10.203.74.100",
  "port": 31820,
  "mtu": 1400
}
```

| Field | Meaning |
| --- | --- |
| `name` | Connection identity used for labels and ownership checks |
| `nodeName` | Name of the new Kubernetes Node |
| `namespace` | Namespace the CLI creates for the connection; defaults to `k0nnect-system` |
| `user` | Currently requires `root` |
| `passwordFile` | Local file containing the SSH password |
| `privateKeyFile` | Alternative: an SSH private key file without a passphrase |
| `image` | Optional k0nnect image for gateway agents, e.g. a release image |

The address fields match the table above. Names start with a lowercase ASCII letter, followed by lowercase letters, numbers, or hyphens, up to 41 characters. Without `image`, the CLI ships its own binary in ConfigMaps into `ubuntu:24.04` containers that install the network tools at start. Changing an enrolled node's transport address requires a controlled disconnect first.

## MTU

`mtu` controls the gateway interface `k0ngw0` and the default route inside the worker namespace. The server's `eth1` uses the original node network's MTU: gateways publish it in the `gateway-state` ConfigMap of their namespace, and the controller selects the minimum.

CNIs that derive Pod MTU from `eth1` can then use a consistent Pod MTU across locations. Oversized outer packets are fragmented. CNI MTU configuration itself is not patched; nested encapsulation and fragmentation are checked by TCP/UDP probes.

## Where state lives

The controller reports progress in the `status` of the VDSConnection. Gateway settings and published keys live in ConfigMaps `gateway-network` and `gateway-state` in `k0nnect-system`.

On the server, `/etc/k0nnect` holds network configuration, the private WireGuard key, ownership marker, resolver, and bootstrap file. These files are intended for root only.
