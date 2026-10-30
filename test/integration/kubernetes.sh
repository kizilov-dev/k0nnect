#!/usr/bin/env bash
# Run explicitly against a prepared lab cluster. Creates only a fresh namespace.
set -euo pipefail
if [[ $# != 2 || ! $1 =~ ^[a-z0-9][a-z0-9.-]*$ || ! $2 =~ ^[a-z0-9][a-z0-9.-]*$ || $1 == "$2" ]]; then
  echo 'Usage: bash test/integration/kubernetes.sh LOCAL_WORKER REMOTE_WORKER' >&2
  exit 2
fi
command -v kubectl >/dev/null
task_namespace="k8nnect-e2e-$(date +%s)-$$"
kubectl create namespace "$task_namespace"
cleanup() { kubectl delete namespace "$task_namespace" --wait=false; }
trap cleanup EXIT
for pair in "local:$1" "remote:$2"; do
  task_name=${pair%%:*}
  task_node=${pair#*:}
  kubectl -n "$task_namespace" apply -f - <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: $task_name
  labels:
    app: k8nnect-e2e
spec:
  nodeName: $task_node
  containers:
    - name: http
      image: busybox:1.37.0
      command: [sh, -c, 'dd if=/dev/zero of=/tmp/payload bs=1024 count=64; httpd -f -p 8080 -h /tmp']
      readinessProbe:
        tcpSocket:
          port: 8080
        initialDelaySeconds: 1
        periodSeconds: 2
EOF
done
kubectl -n "$task_namespace" apply -f - <<'EOF'
apiVersion: v1
kind: Service
metadata:
  name: remote
spec:
  selector:
    app: k8nnect-e2e
    target: remote
  ports:
    - port: 8080
      targetPort: 8080
EOF
kubectl -n "$task_namespace" label pod remote target=remote
kubectl -n "$task_namespace" wait --for=condition=Ready pod --all --timeout=180s
for pair in local:remote remote:local; do
  task_from=${pair%%:*}
  task_to=${pair#*:}
  task_ip=$(kubectl -n "$task_namespace" get pod "$task_to" -o jsonpath='{.status.podIP}')
  task_bytes=$(kubectl -n "$task_namespace" exec "$task_from" -- sh -c 'wget -T 10 -qO- "$1" | wc -c' sh "http://$task_ip:8080/payload")
  [[ $task_bytes == 65536 ]] || { echo 'Incomplete Pod-to-Pod transfer' >&2; exit 1; }
done
task_bytes=$(kubectl -n "$task_namespace" exec local -- sh -c 'wget -T 10 -qO- http://remote:8080/payload | wc -c')
[[ $task_bytes == 65536 ]]
kubectl -n "$task_namespace" logs remote >/dev/null
echo 'PASS: bidirectional Pod TCP, 64KiB payload, Service DNS, exec and logs'
echo 'Still required separately: UDP workloads, partitions, reboot, revocation and performance.'
