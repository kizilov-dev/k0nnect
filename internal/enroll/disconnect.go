package enroll

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"k0nnect/internal/kube"
)

func RemoveNetwork(ctx context.Context, kind, path string) error {
	n, e := loadNetwork(path)
	if e != nil {
		return e
	}
	if kind == "worker-remove" {
		for _, target := range []struct{ ns, device string }{{"", "k0nworker0"}, {WorkerNS, "eth1"}, {WorkerNS, "k0nworker0"}} {
			// The namespace may already have been removed after a previous successful detach.
			if target.ns != "" {
				if _, err := command(ctx, "ip", "-n", target.ns, "link", "show"); err != nil {
					continue
				}
			}
			exists, err := linkExists(ctx, target.ns, target.device, "k0nnect:worker:"+n.Name)
			if err != nil {
				return err
			}
			if exists {
				args := []string{"link", "delete", target.device}
				if target.ns != "" {
					args = append([]string{"-n", target.ns}, args...)
				}
				if _, err = command(ctx, "ip", args...); err != nil {
					return err
				}
			}
		}
		if _, err := command(ctx, "ip", "-n", WorkerNS, "link", "show"); err == nil {
			_, e = command(ctx, "ip", "netns", "delete", WorkerNS)
		}
		return e
	}
	exists, e := linkExists(ctx, "", GatewayInterface, "k0nnect:gateway:"+n.Name)
	if e != nil {
		return e
	}
	if exists {
		if _, e = command(ctx, "ip", "link", "delete", GatewayInterface); e != nil {
			return e
		}
	}
	return deleteRules(ctx, append(gatewayRules(n), legacyGatewayRules(n)...))
}

func Disconnect(ctx context.Context, k *kube.Client, c Config, drain bool, out io.Writer) error {
	if !drain {
		return fmt.Errorf("disconnect requires --drain; workloads are evicted with PodDisruptionBudgets honored")
	}
	if e := c.Validate(); e != nil {
		return e
	}
	namespace, e := k.Get(ctx, "/api/v1/namespaces/"+c.Namespace)
	if e != nil {
		return e
	}
	if kube.Str(kube.Map(kube.Map(namespace, "metadata"), "annotations"), "k0nnect.io/connection") != c.Name {
		return fmt.Errorf("foreign namespace")
	}
	ns := "/api/v1/namespaces/" + c.Namespace
	dep := "/apis/apps/v1/namespaces/" + c.Namespace + "/deployments/installer"
	if d, err := k.Get(ctx, dep); err == nil {
		if kube.Str(kube.Map(kube.Map(d, "metadata"), "labels"), "k0nnect.io/connection") != c.Name {
			return fmt.Errorf("foreign installer Deployment")
		}
		if _, e = k.Do(ctx, "PATCH", dep, kube.Object{"spec": kube.Object{"replicas": 0}}); e != nil {
			return e
		}
	} else if !kube.Code(err, 404) {
		return err
	}
	nodePath := "/api/v1/nodes/" + c.NodeName
	if node, err := k.Get(ctx, nodePath); err == nil {
		if kube.Str(kube.Map(kube.Map(node, "metadata"), "labels"), "k0nnect.io/connection") != c.Name {
			return fmt.Errorf("foreign node")
		}
		if _, e = k.Do(ctx, "PATCH", nodePath, kube.Object{"spec": kube.Object{"unschedulable": true}}); e != nil {
			return e
		}
		fmt.Fprintln(out, "Cordoned external node; evicting non-DaemonSet workloads")
		deadline := time.Now().Add(3 * time.Minute)
		for {
			pods, err := k.Get(ctx, "/api/v1/pods?fieldSelector=spec.nodeName%3D"+c.NodeName)
			if err != nil {
				return err
			}
			pending := 0
			for _, v := range kube.Items(pods, "items") {
				pod := kube.As(v)
				meta := kube.Map(pod, "metadata")
				skip := false
				for _, ref := range kube.Items(meta, "ownerReferences") {
					if kube.Str(kube.As(ref), "kind") == "DaemonSet" {
						skip = true
					}
				}
				if skip || kube.Str(kube.Map(meta, "annotations"), "kubernetes.io/config.mirror") != "" {
					continue
				}
				pending++
				eviction := kube.Object{"apiVersion": "policy/v1", "kind": "Eviction", "metadata": kube.Object{"name": kube.Str(meta, "name"), "namespace": kube.Str(meta, "namespace")}}
				if _, err = k.Do(ctx, "POST", "/api/v1/namespaces/"+kube.Str(meta, "namespace")+"/pods/"+kube.Str(meta, "name")+"/eviction", eviction); err != nil && !kube.Code(err, 404) {
					return fmt.Errorf("eviction refused; worker remains cordoned and running: %w", err)
				}
			}
			if pending == 0 {
				break
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("drain timed out; worker remains running")
			}
			if e = pause(ctx, 3*time.Second); e != nil {
				return e
			}
		}
	} else if !kube.Code(err, 404) {
		return err
	}
	r, e := Dial(ctx, c)
	if e != nil {
		return e
	}
	defer r.Close()
	stop := stopWorkerScript(c)
	if _, e = r.Run(ctx, stop, nil); e != nil {
		return fmt.Errorf("stop owned Worker services: %w", e)
	}
	if e = ownedDelete(ctx, k, nodePath, c.Name); e != nil {
		return e
	}
	fmt.Fprintln(out, "Worker stopped; cleaning owned gateway networking")
	if e = ownedDelete(ctx, k, "/apis/apps/v1/namespaces/"+c.Namespace+"/daemonsets/gateway", c.Name); e != nil {
		return e
	}
	for {
		pods, err := k.Get(ctx, ns+"/pods?labelSelector=app%3Dk0nnect-gateway")
		if err != nil {
			return err
		}
		if len(kube.Items(pods, "items")) == 0 {
			break
		}
		if e = pause(ctx, 2*time.Second); e != nil {
			return e
		}
	}
	cm, e := k.Get(ctx, ns+"/configmaps/gateway-network")
	if e != nil && !kube.Code(e, 404) {
		return e
	}
	if e == nil {
		sources, err := payload(ctx, k, c)
		if err != nil {
			return err
		}
		for name, raw := range kube.Map(cm, "data") {
			var network Network
			s, _ := raw.(string)
			if err = json.Unmarshal([]byte(s), &network); err != nil || network.Name != c.Name || network.NodeName != name {
				return fmt.Errorf("invalid owned gateway cleanup configuration")
			}
			jobName := "cleanup-" + strings.ReplaceAll(name, ".", "-")
			labels := kube.Object{"k0nnect.io/connection": c.Name}
			startup := "apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends wireguard-tools iproute2 iptables && cat /payload/part* | gzip -d > /tmp/k0nnect && chmod 0700 /tmp/k0nnect && exec /tmp/k0nnect gateway-remove --config /network/network.json"
			job := kube.Object{"apiVersion": "batch/v1", "kind": "Job", "metadata": kube.Object{"name": jobName, "namespace": c.Namespace, "labels": labels}, "spec": kube.Object{"backoffLimit": 0, "activeDeadlineSeconds": 180, "template": kube.Object{"metadata": kube.Object{"labels": labels}, "spec": kube.Object{"nodeName": name, "hostNetwork": true, "restartPolicy": "Never", "automountServiceAccountToken": false, "tolerations": []any{kube.Object{"operator": "Exists"}}, "containers": []any{kube.Object{"name": "cleanup", "image": "ubuntu:24.04", "command": []string{"bash", "-ec", startup}, "securityContext": kube.Object{"capabilities": kube.Object{"add": []string{"NET_ADMIN"}}}, "volumeMounts": []any{kube.Object{"name": "payload", "mountPath": "/payload", "readOnly": true}, kube.Object{"name": "network", "mountPath": "/network", "readOnly": true}}}}, "volumes": []any{kube.Object{"name": "payload", "projected": kube.Object{"sources": sources}}, kube.Object{"name": "network", "configMap": kube.Object{"name": "gateway-network", "items": []any{kube.Object{"key": name, "path": "network.json"}}}}}}}}}
			jobPath := "/apis/batch/v1/namespaces/" + c.Namespace + "/jobs"
			if _, err = k.Do(ctx, "POST", jobPath, job); err != nil {
				return err
			}
			for {
				obj, err := k.Get(ctx, jobPath+"/"+jobName)
				if err != nil {
					return err
				}
				status := kube.Map(obj, "status")
				if v, ok := status["succeeded"].(float64); ok && v > 0 {
					break
				}
				if v, ok := status["failed"].(float64); ok && v > 0 {
					return fmt.Errorf("gateway cleanup Job failed on %s", name)
				}
				if err = pause(ctx, 3*time.Second); err != nil {
					return err
				}
			}
		}
	}
	if e = ownedDelete(ctx, k, "/api/v1/namespaces/kube-system/secrets/"+tokenName(c), c.Name); e != nil {
		return e
	}
	profiles, e := k.Get(ctx, "/api/v1/namespaces/kube-system/configmaps?labelSelector=k0nnect.io%2Fconnection%3D"+c.Name)
	if e != nil {
		return e
	}
	for _, v := range kube.Items(profiles, "items") {
		obj := kube.As(v)
		if e = ownedDelete(ctx, k, "/api/v1/namespaces/kube-system/configmaps/"+kube.Str(kube.Map(obj, "metadata"), "name"), c.Name); e != nil {
			return e
		}
	}
	for _, kind := range []string{"clusterrolebindings", "clusterroles"} {
		for _, suffix := range []string{"-gateway", "-installer"} {
			if e = ownedDelete(ctx, k, "/apis/rbac.authorization.k8s.io/v1/"+kind+"/"+c.Namespace+suffix, c.Name); e != nil {
				return e
			}
		}
	}
	for _, kind := range []string{"rolebindings", "roles"} {
		for _, name := range []string{c.Namespace + "-installer-local", c.Namespace + "-worker-profile"} {
			if e = ownedDelete(ctx, k, "/apis/rbac.authorization.k8s.io/v1/namespaces/kube-system/"+kind+"/"+name, c.Name); e != nil {
				return e
			}
		}
	}
	if e = k.Delete(ctx, "/api/v1/namespaces/"+c.Namespace); e != nil {
		return e
	}
	fmt.Fprintln(out, "Disconnected. K0s data, installed packages and owner-only keys on the VDS are preserved for recovery; no user data was reset.")
	return nil
}

// stopWorkerScript stops the managed Worker on the VDS and removes the runtime
// state of the deleted Node, keeping installed binaries and the managed marker.
func stopWorkerScript(c Config) string {
	return `if test -f /etc/k0nnect/managed.json; then
python3 -c ` + Quote("import json; assert json.load(open('/etc/k0nnect/managed.json'))['name']=="+fmt.Sprintf("%q", c.Name)) + `
if systemctl is-active --quiet k0sworker.service; then
 TASK_K0NNECT_IDS=$(/usr/local/bin/k0s ctr --namespace=k8s.io tasks list -q)
 # kubelet may delete a task between list and kill; k0sworker is stopped below anyway.
 for TASK_K0NNECT_ID in $TASK_K0NNECT_IDS; do /usr/local/bin/k0s ctr --namespace=k8s.io tasks kill --signal SIGKILL --all "$TASK_K0NNECT_ID" || true; done
fi
for TASK_K0NNECT_UNIT in k0sworker.service k0nnect-agent.service k0nnect-network.service; do
 if systemctl cat "$TASK_K0NNECT_UNIT" >/dev/null 2>&1; then systemctl stop "$TASK_K0NNECT_UNIT"; systemctl disable "$TASK_K0NNECT_UNIT"; fi
done
# containerd shims outlive k0sworker. They and their containers would keep the old
# Worker network namespace, with CNI devices of the deleted Node, alive.
for TASK_K0NNECT_PID in $(pgrep -f '^/var/lib/k0s/bin/containerd-shim-runc-v2 .*-address /run/k0s/containerd\.sock' || true); do
 pkill -KILL -P "$TASK_K0NNECT_PID" || true
 kill -KILL "$TASK_K0NNECT_PID" || true
done
# Node-local CNI state belongs to the deleted Node: a reconnected Node receives a
# new Pod CIDR, and stale subnet leases or bridges would block Pod sandboxes.
# Calico and Cilium mount cgroup2 below their state directories.
for TASK_K0NNECT_MOUNT in $(findmnt -rn -o TARGET | grep -E '^/run/(calico|cilium)(/|$)' | sort -r); do umount "$TASK_K0NNECT_MOUNT"; done
rm -rf /etc/cni/net.d /var/lib/cni /run/flannel /var/run/calico /var/lib/calico /var/run/cilium /var/lib/cilium /var/lib/kube-router /sys/fs/bpf/cilium
rm -f /sys/fs/bpf/tc/globals/cilium_*
if test -f /etc/k0nnect/worker.json; then /usr/local/bin/k0nnect worker-remove --config /etc/k0nnect/worker.json; fi
fi`
}
