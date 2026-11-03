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
	if e = namespaceOwned(namespace, c); e != nil {
		return e
	}
	ns := "/api/v1/namespaces/" + c.Namespace
	if !c.InCluster {
		if e = stopLegacyInstaller(ctx, k, c); e != nil {
			return e
		}
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
	if r, err := Dial(ctx, c); err == nil {
		defer r.Close()
		if _, e = r.Run(ctx, stopWorkerScript(c), nil); e != nil {
			return fmt.Errorf("stop owned Worker services: %w", e)
		}
	} else {
		// Connect starts the Worker network only after publishing the gateway
		// network and registers the Node after that; without both nothing
		// runs on the VDS, e.g. when its address or credentials were wrong.
		_, nodeErr := k.Get(ctx, nodePath)
		_, networkErr := k.Get(ctx, ns+"/configmaps/gateway-network")
		if !kube.Code(nodeErr, 404) || !kube.Code(networkErr, 404) {
			return err
		}
		fmt.Fprintln(out, "VDS unreachable, but no Worker or network was started there; removing cluster objects only:", err)
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
		container, volumes, err := agentContainer(ctx, k, c, "cleanup", "wireguard-tools iproute2 iptables", "gateway-remove", "--config", "/network/network.json")
		if err != nil {
			return err
		}
		container["securityContext"] = kube.Object{"capabilities": kube.Object{"add": []string{"NET_ADMIN"}}}
		container["volumeMounts"] = append(kube.Items(container, "volumeMounts"), kube.Object{"name": "network", "mountPath": "/network", "readOnly": true})
		for name, raw := range kube.Map(cm, "data") {
			var network Network
			s, _ := raw.(string)
			if err = json.Unmarshal([]byte(s), &network); err != nil || network.Name != c.Name || network.NodeName != name {
				return fmt.Errorf("invalid owned gateway cleanup configuration")
			}
			jobName := "cleanup-" + strings.ReplaceAll(name, ".", "-")
			labels := kube.Object{"k0nnect.io/connection": c.Name}
			podVolumes := append(append([]any{}, volumes...), kube.Object{"name": "network", "configMap": kube.Object{"name": "gateway-network", "items": []any{kube.Object{"key": name, "path": "network.json"}}}})
			job := kube.Object{"apiVersion": "batch/v1", "kind": "Job", "metadata": kube.Object{"name": jobName, "namespace": c.Namespace, "labels": labels}, "spec": kube.Object{"backoffLimit": 0, "activeDeadlineSeconds": 180, "template": kube.Object{"metadata": kube.Object{"labels": labels}, "spec": kube.Object{"nodeName": name, "hostNetwork": true, "restartPolicy": "Never", "automountServiceAccountToken": false, "tolerations": []any{kube.Object{"operator": "Exists"}}, "containers": []any{container}, "volumes": podVolumes}}}}
			jobPath := "/apis/batch/v1/namespaces/" + c.Namespace + "/jobs"
			// A cleanup interrupted by a controller restart leaves its Job behind;
			// the removal is idempotent, so the Job is recreated.
			if err = ownedDelete(ctx, k, jobPath+"/"+jobName, c.Name); err != nil {
				return err
			}
			for _, err = k.Get(ctx, jobPath+"/"+jobName); err == nil; _, err = k.Get(ctx, jobPath+"/"+jobName) {
				if err = pause(ctx, 2*time.Second); err != nil {
					return err
				}
			}
			if !kube.Code(err, 404) {
				return err
			}
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
	// Profiles are removed by name: the controller may not list kube-system ConfigMaps.
	for _, minor := range supportedMinors {
		if e = ownedDelete(ctx, k, "/api/v1/namespaces/kube-system/configmaps/"+profileConfigMap(c, minor), c.Name); e != nil {
			return e
		}
	}
	suffixes, roles := []string{"-gateway", "-installer"}, []string{c.Namespace + "-installer-local", c.Namespace + "-worker-profile"}
	if c.InCluster {
		suffixes, roles = suffixes[:1], roles[1:]
	}
	for _, kind := range []string{"clusterrolebindings", "clusterroles"} {
		for _, suffix := range suffixes {
			if e = ownedDelete(ctx, k, "/apis/rbac.authorization.k8s.io/v1/"+kind+"/"+c.Namespace+suffix, c.Name); e != nil {
				return e
			}
		}
	}
	for _, kind := range []string{"rolebindings", "roles"} {
		for _, name := range roles {
			if e = ownedDelete(ctx, k, "/apis/rbac.authorization.k8s.io/v1/namespaces/kube-system/"+kind+"/"+name, c.Name); e != nil {
				return e
			}
		}
	}
	if c.InCluster {
		// The controller's namespace and its own objects come from the install
		// manifest and carry no connection label.
		e = deleteLabeled(ctx, k, c)
	} else {
		e = k.Delete(ctx, "/api/v1/namespaces/"+c.Namespace)
	}
	if e != nil {
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

// Installations made by the former addon-install command run an installer
// Deployment that would reconnect the Worker during disconnect.
func stopLegacyInstaller(ctx context.Context, k *kube.Client, c Config) error {
	dep := "/apis/apps/v1/namespaces/" + c.Namespace + "/deployments/installer"
	d, e := k.Get(ctx, dep)
	if kube.Code(e, 404) {
		return nil
	}
	if e != nil {
		return e
	}
	if kube.Str(kube.Map(kube.Map(d, "metadata"), "labels"), "k0nnect.io/connection") != c.Name {
		return fmt.Errorf("foreign installer Deployment")
	}
	_, e = k.Do(ctx, "PATCH", dep, kube.Object{"spec": kube.Object{"replicas": 0}})
	return e
}

func deleteLabeled(ctx context.Context, k *kube.Client, c Config) error {
	for _, collection := range []string{"/apis/batch/v1/namespaces/%s/jobs", "/apis/apps/v1/namespaces/%s/daemonsets", "/api/v1/namespaces/%s/pods", "/api/v1/namespaces/%s/services", "/api/v1/namespaces/%s/configmaps", "/api/v1/namespaces/%s/serviceaccounts", "/apis/rbac.authorization.k8s.io/v1/namespaces/%s/rolebindings", "/apis/rbac.authorization.k8s.io/v1/namespaces/%s/roles"} {
		path := fmt.Sprintf(collection, c.Namespace)
		list, e := k.Get(ctx, path+"?labelSelector=k0nnect.io%2Fconnection%3D"+c.Name)
		if e != nil {
			return e
		}
		for _, v := range kube.Items(list, "items") {
			if e = ownedDelete(ctx, k, path+"/"+kube.Str(kube.Map(kube.As(v), "metadata"), "name"), c.Name); e != nil {
				return e
			}
		}
	}
	return nil
}
