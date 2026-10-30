# Configuration

The CLI reads a connection JSON file. Use the same file for checks, enrollment, addon installation, verification, and removal.

## Example

The address and fingerprint below are placeholders. Replace them with your server's actual address and verified SSH fingerprint.

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

## Identity and SSH access

| Field | Meaning |
| --- | --- |
| `name` | Connection identity used for labels and ownership checks |
| `nodeName` | Name of the new Kubernetes Node |
| `namespace` | Addon namespace; defaults to `k0nnect-system` |
| `host` | Literal server IPv4 address; hostnames are not supported |
| `user` | Currently requires `root` |
| `sshPort` | SSH port; defaults to 22 |
| `hostKeySHA256` | Pinned host fingerprint; the tested preflight uses Ed25519 |
| `passwordFile` | Local file containing the SSH password |
| `privateKeyFile` | Alternative: an SSH private key file without a passphrase |

Names start with a lowercase ASCII letter, followed by lowercase letters, numbers, or hyphens. The maximum length is 41 characters.

## Addresses and ports

| Field | Constraint |
| --- | --- |
| `transportCIDR` | Non-overlapping, canonical private IPv4 /24 |
| `workerAddress` | Inside transportCIDR, with last octet 100–254 |
| `port` | Gateway UDP port in 30000–32766; defaults to 31820 |
| `mtu` | WireGuard transport MTU, 1280–1440; defaults to 1400 |
| `k0sVersion` | Optional k0s release; normally discovered from the worker profile |

The server listens on `port + 1`. Initial /24 addresses are assigned to gateways, leaving `.100` onward for the worker. Changing an enrolled node's transport address requires a controlled disconnect first.

## MTU

`mtu` controls the gateway interface `k0ngw0` and the default route inside the worker namespace. The server's `eth1` uses the original node network's MTU: gateways publish it in `k0nnect.io/node-mtu`, and the installer selects the minimum.

CNIs that derive Pod MTU from `eth1` can then use a consistent Pod MTU across locations. Oversized outer packets are fragmented. CNI MTU configuration itself is not patched; nested encapsulation and fragmentation are checked by TCP/UDP probes.

## Where configuration lives

The addon mounts public connection settings from ConfigMap `connection` and SSH credentials from Secret `vds-ssh`. ConfigMap `connection-status` contains reconciliation status.

On the server, `/etc/k0nnect` holds network configuration, the private WireGuard key, ownership marker, resolver, and bootstrap file. These files are intended for root only.
