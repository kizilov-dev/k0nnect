package enroll

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"

	"k8nnect/internal/kube"
)

func ensureNamespace(ctx context.Context, k *kube.Client, c Config) error {
	path := "/api/v1/namespaces/" + c.Namespace
	existing, e := k.Get(ctx, path)
	if e == nil {
		if kube.Str(kube.Map(kube.Map(existing, "metadata"), "annotations"), "k8nnect.io/connection") != c.Name {
			return fmt.Errorf("namespace is not owned by this connection")
		}
		return nil
	}
	if !kube.Code(e, 404) {
		return e
	}
	_, e = k.Do(ctx, "POST", "/api/v1/namespaces", kube.Object{"apiVersion": "v1", "kind": "Namespace", "metadata": kube.Object{"name": c.Namespace, "labels": kube.Object{"app.kubernetes.io/managed-by": "k8nnect", "pod-security.kubernetes.io/enforce": "privileged"}, "annotations": kube.Object{"k8nnect.io/connection": c.Name}}})
	return e
}
func payload(ctx context.Context, k *kube.Client, c Config) ([]any, error) {
	path, e := os.Executable()
	if e != nil {
		return nil, e
	}
	binary, e := os.ReadFile(path)
	if e != nil {
		return nil, e
	}
	var zipped bytes.Buffer
	gz := gzip.NewWriter(&zipped)
	if _, e = gz.Write(binary); e != nil {
		return nil, e
	}
	if e = gz.Close(); e != nil {
		return nil, e
	}
	sum := fmt.Sprintf("%x", sha256.Sum256(binary))
	data := zipped.Bytes()
	var sources []any
	for index, start := 0, 0; start < len(data); index, start = index+1, start+400*1024 {
		end := start + 400*1024
		if end > len(data) {
			end = len(data)
		}
		key := fmt.Sprintf("part%03d", index)
		name := fmt.Sprintf("binary-%s-%03d", sum[:12], index)
		collection := "/api/v1/namespaces/" + c.Namespace + "/configmaps"
		if _, e = k.Get(ctx, collection+"/"+name); kube.Code(e, 404) {
			_, e = k.Do(ctx, "POST", collection, kube.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": kube.Object{"name": name, "namespace": c.Namespace, "labels": kube.Object{"app.kubernetes.io/managed-by": "k8nnect"}}, "immutable": true, "binaryData": kube.Object{key: base64.StdEncoding.EncodeToString(data[start:end])}})
			if e != nil {
				return nil, e
			}
		} else if e != nil {
			return nil, e
		}
		sources = append(sources, kube.Object{"configMap": kube.Object{"name": name}})
	}
	return sources, nil
}
func gatewayResources(ctx context.Context, k *kube.Client, c Config, networks map[string]Network, nodes []string) error {
	ns := "/api/v1/namespaces/" + c.Namespace
	labels := kube.Object{"app": "k8nnect-gateway", "k8nnect.io/connection": c.Name}
	data := kube.Object{}
	for name, n := range networks {
		b, e := json.Marshal(n)
		if e != nil {
			return e
		}
		data[name] = string(b)
	}
	if e := applyOwned(ctx, k, c, ns+"/configmaps", "gateway-network", kube.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": kube.Object{"name": "gateway-network", "namespace": c.Namespace}, "data": data}); e != nil {
		return e
	}
	if e := applyOwned(ctx, k, c, ns+"/serviceaccounts", "gateway", kube.Object{"apiVersion": "v1", "kind": "ServiceAccount", "metadata": kube.Object{"name": "gateway", "namespace": c.Namespace}}); e != nil {
		return e
	}
	role := c.Namespace + "-gateway"
	cr := kube.Object{"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "ClusterRole", "metadata": kube.Object{"name": role}, "rules": []any{kube.Object{"apiGroups": []string{""}, "resources": []string{"nodes"}, "resourceNames": nodes, "verbs": []string{"get", "patch"}}}}
	if e := applyOwned(ctx, k, c, "/apis/rbac.authorization.k8s.io/v1/clusterroles", role, cr); e != nil {
		return e
	}
	binding := kube.Object{"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "ClusterRoleBinding", "metadata": kube.Object{"name": role}, "roleRef": kube.Object{"apiGroup": "rbac.authorization.k8s.io", "kind": "ClusterRole", "name": role}, "subjects": []any{kube.Object{"kind": "ServiceAccount", "name": "gateway", "namespace": c.Namespace}}}
	if e := applyOwned(ctx, k, c, "/apis/rbac.authorization.k8s.io/v1/clusterrolebindings", role, binding); e != nil {
		return e
	}
	readRole := kube.Object{"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "Role", "metadata": kube.Object{"name": "gateway-config", "namespace": c.Namespace}, "rules": []any{kube.Object{"apiGroups": []string{""}, "resources": []string{"configmaps"}, "resourceNames": []string{"gateway-network"}, "verbs": []string{"get"}}}}
	if e := applyOwned(ctx, k, c, "/apis/rbac.authorization.k8s.io/v1/namespaces/"+c.Namespace+"/roles", "gateway-config", readRole); e != nil {
		return e
	}
	rb := kube.Object{"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "RoleBinding", "metadata": kube.Object{"name": "gateway-config", "namespace": c.Namespace}, "roleRef": kube.Object{"apiGroup": "rbac.authorization.k8s.io", "kind": "Role", "name": "gateway-config"}, "subjects": []any{kube.Object{"kind": "ServiceAccount", "name": "gateway", "namespace": c.Namespace}}}
	if e := applyOwned(ctx, k, c, "/apis/rbac.authorization.k8s.io/v1/namespaces/"+c.Namespace+"/rolebindings", "gateway-config", rb); e != nil {
		return e
	}
	sources, e := payload(ctx, k, c)
	if e != nil {
		return e
	}
	startup := "apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends wireguard-tools iproute2 iptables ca-certificates && cat /payload/part* | gzip -d > /tmp/k8nnect && chmod 0700 /tmp/k8nnect && exec /tmp/k8nnect gateway-agent"
	// Node field selectors accept one value per term; terms are ORed.
	terms := make([]any, 0, len(nodes))
	for _, node := range nodes {
		terms = append(terms, kube.Object{"matchFields": []any{kube.Object{"key": "metadata.name", "operator": "In", "values": []string{node}}}})
	}
	podSpec := kube.Object{"serviceAccountName": "gateway", "hostNetwork": true, "dnsPolicy": "ClusterFirstWithHostNet", "tolerations": []any{kube.Object{"operator": "Exists"}}, "affinity": kube.Object{"nodeAffinity": kube.Object{"requiredDuringSchedulingIgnoredDuringExecution": kube.Object{"nodeSelectorTerms": terms}}}, "containers": []any{kube.Object{"name": "gateway", "image": "ubuntu:24.04", "command": []string{"bash", "-ec", startup}, "env": []any{kube.Object{"name": "NODE_NAME", "valueFrom": kube.Object{"fieldRef": kube.Object{"fieldPath": "spec.nodeName"}}}, kube.Object{"name": "POD_NAMESPACE", "value": c.Namespace}}, "securityContext": kube.Object{"capabilities": kube.Object{"add": []string{"NET_ADMIN"}}, "allowPrivilegeEscalation": false}, "resources": kube.Object{"requests": kube.Object{"cpu": "25m", "memory": "32Mi"}, "limits": kube.Object{"memory": "256Mi"}}, "volumeMounts": []any{kube.Object{"name": "payload", "mountPath": "/payload", "readOnly": true}, kube.Object{"name": "state", "mountPath": "/state"}}, "readinessProbe": kube.Object{"exec": kube.Object{"command": []string{"test", "-f", "/tmp/k8nnect-ready"}}, "periodSeconds": 3}}}, "volumes": []any{kube.Object{"name": "payload", "projected": kube.Object{"sources": sources}}, kube.Object{"name": "state", "hostPath": kube.Object{"path": "/var/lib/k8nnect/" + c.Name, "type": "DirectoryOrCreate"}}}}
	ds := kube.Object{"apiVersion": "apps/v1", "kind": "DaemonSet", "metadata": kube.Object{"name": "gateway", "namespace": c.Namespace}, "spec": kube.Object{"selector": kube.Object{"matchLabels": labels}, "template": kube.Object{"metadata": kube.Object{"labels": labels}, "spec": podSpec}}}
	if e = applyOwned(ctx, k, c, "/apis/apps/v1/namespaces/"+c.Namespace+"/daemonsets", "gateway", ds); e != nil {
		return e
	}
	service := kube.Object{"apiVersion": "v1", "kind": "Service", "metadata": kube.Object{"name": "gateway", "namespace": c.Namespace}, "spec": kube.Object{"type": "NodePort", "externalTrafficPolicy": "Local", "selector": labels, "ports": []any{kube.Object{"name": "wireguard", "protocol": "UDP", "port": c.Port, "targetPort": c.Port, "nodePort": c.Port}}}}
	return applyOwned(ctx, k, c, ns+"/services", "gateway", service)
}
