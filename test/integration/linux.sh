#!/usr/bin/env bash
# Opt-in, privileged test. Only creates disposable namespaces and veth devices.
set -euo pipefail
if [[ ${EUID} != 0 ]]; then
  echo 'SKIP: requires root, iproute2, wireguard-tools, ping and kernel WireGuard' >&2
  exit 77
fi
for tool in ip wg ping python3; do
  command -v "$tool" >/dev/null || { echo "SKIP: missing $tool" >&2; exit 77; }
done
task_root=$(cd "$(dirname "$0")/../.." && pwd)
task_bin="$task_root/bin/k0nnect"
[[ -x "$task_bin" ]] || { echo 'Run make build first' >&2; exit 1; }
task_dir=$(mktemp -d)
task_ns_a="k0n-a-$$"
task_ns_b="k0n-b-$$"
task_created_a=false
task_created_b=false
cleanup() {
  if $task_created_a; then ip netns del "$task_ns_a"; fi
  if $task_created_b; then ip netns del "$task_ns_b"; fi
  rm -rf -- "$task_dir"
}
trap cleanup EXIT
umask 077
wg genkey > "$task_dir/a.key"
wg genkey > "$task_dir/b.key"
wg pubkey < "$task_dir/a.key" > "$task_dir/a.pub"
wg pubkey < "$task_dir/b.key" > "$task_dir/b.pub"
python3 - "$task_root" "$task_dir" <<'PY'
import json, sys
from pathlib import Path
root, work = map(Path, sys.argv[1:])
c = json.loads((root/'examples/inventory.json').read_text())
for i, name in enumerate(['a','b']):
    c['nodes'][i]['endpoint'] = f'192.0.2.{i+1}:51820'
    c['nodes'][i]['wireguard'] = {
        'publicKey': (work/f'{name}.pub').read_text().strip(),
        'privateKeyFile': str(work/f'{name}.key')
    }
(work/'inventory.json').write_text(json.dumps(c))
PY
ip netns add "$task_ns_a"
task_created_a=true
ip netns add "$task_ns_b"
task_created_b=true
ip -n "$task_ns_a" link add eth0 type veth peer name eth0 netns "$task_ns_b"
ip -n "$task_ns_a" address add 192.0.2.1/24 dev eth0
ip -n "$task_ns_b" address add 192.0.2.2/24 dev eth0
for ns in "$task_ns_a" "$task_ns_b"; do
  ip -n "$ns" link set lo up
  ip -n "$ns" link set eth0 up
done
invoke() {
  local ns=$1 node=$2 command=$3
  shift 3
  ip netns exec "$ns" "$task_bin" "$command" --config "$task_dir/inventory.json" --node "$node" "$@"
}
invoke "$task_ns_a" control-plane doctor
invoke "$task_ns_a" control-plane apply
invoke "$task_ns_b" remote-worker apply
invoke "$task_ns_a" control-plane verify
invoke "$task_ns_b" remote-worker verify
invoke "$task_ns_a" control-plane apply
[[ $(ip -n "$task_ns_a" -4 route show dev k0n0 | wc -l) == 1 ]]
# Restart reconciliation after loss of interface admin state.
ip -n "$task_ns_a" link set k0n0 down
invoke "$task_ns_a" control-plane apply
invoke "$task_ns_a" control-plane verify
# A foreign interface must survive a rejected apply/remove.
invoke "$task_ns_a" control-plane remove --drained
ip -n "$task_ns_a" link add k0n0 type dummy
if invoke "$task_ns_a" control-plane apply; then echo 'Foreign interface accepted' >&2; exit 1; fi
if invoke "$task_ns_a" control-plane remove --drained; then echo 'Foreign interface removed' >&2; exit 1; fi
ip -n "$task_ns_a" link show k0n0 >/dev/null
invoke "$task_ns_b" remote-worker remove --drained
echo 'PASS: encrypted link, MTU, repeat apply, recovery, removal and foreign ownership'
