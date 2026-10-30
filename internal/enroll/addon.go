package enroll

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"k8nnect/internal/kube"
)

func InstallAddon(ctx context.Context, k *kube.Client, c Config, out io.Writer) error {
	if e := c.Validate(); e != nil {
		return e
	}
	if c.K0sVersion == "" {
		if _, _, _, e := discover(ctx, k, &c); e != nil {
			return e
		}
	}
	if e := ensureNamespace(ctx, k, c); e != nil {
		return e
	}
	credentials := kube.Object{}
	if c.PasswordFile != "" {
		b, e := os.ReadFile(c.PasswordFile)
		if e != nil {
			return e
		}
		credentials["password"] = string(b)
		c.PasswordFile = "/credentials/password"
	}
	if c.PrivateKeyFile != "" {
		b, e := os.ReadFile(c.PrivateKeyFile)
		if e != nil {
			return e
		}
		credentials["private-key"] = string(b)
		c.PrivateKeyFile = "/credentials/private-key"
	}
	if len(credentials) == 0 {
		return fmt.Errorf("addon requires a passwordFile or privateKeyFile; SSH agent cannot be mounted into the cluster")
	}
	ns := "/api/v1/namespaces/" + c.Namespace
	secret := kube.Object{"apiVersion": "v1", "kind": "Secret", "metadata": kube.Object{"name": "vds-ssh", "namespace": c.Namespace, "labels": kube.Object{"k8nnect.io/connection": c.Name}}, "type": "Opaque", "stringData": credentials}
	if e := applyOwned(ctx, k, c, ns+"/secrets", "vds-ssh", secret); e != nil {
		return e
	}
	b, e := json.Marshal(c)
	if e != nil {
		return e
	}
	if e = applyOwned(ctx, k, c, ns+"/configmaps", "connection", kube.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": kube.Object{"name": "connection", "namespace": c.Namespace}, "data": kube.Object{"connection.json": string(b)}}); e != nil {
		return e
	}
	if e = applyOwned(ctx, k, c, ns+"/serviceaccounts", "installer", kube.Object{"apiVersion": "v1", "kind": "ServiceAccount", "metadata": kube.Object{"name": "installer", "namespace": c.Namespace}}); e != nil {
		return e
	}
	role := c.Namespace + "-installer"
	rules := []any{
		kube.Object{"apiGroups": []string{""}, "resources": []string{"nodes"}, "verbs": []string{"get", "list", "patch"}},
		kube.Object{"apiGroups": []string{""}, "resources": []string{"namespaces", "services"}, "verbs": []string{"get", "list"}},
		kube.Object{"apiGroups": []string{"apps"}, "resources": []string{"daemonsets"}, "verbs": []string{"get"}},
		kube.Object{"apiGroups": []string{"crd.projectcalico.org"}, "resources": []string{"ippools"}, "verbs": []string{"get", "list"}},
		kube.Object{"apiGroups": []string{"certificates.k8s.io"}, "resources": []string{"certificatesigningrequests"}, "verbs": []string{"get", "list"}},
		kube.Object{"apiGroups": []string{"certificates.k8s.io"}, "resources": []string{"certificatesigningrequests/approval"}, "verbs": []string{"update"}},
		kube.Object{"apiGroups": []string{"certificates.k8s.io"}, "resources": []string{"signers"}, "resourceNames": []string{"kubernetes.io/kubelet-serving"}, "verbs": []string{"approve"}},
		kube.Object{"apiGroups": []string{"rbac.authorization.k8s.io"}, "resources": []string{"clusterroles", "clusterrolebindings"}, "verbs": []string{"get", "create", "patch"}},
	}
	if e = applyOwned(ctx, k, c, "/apis/rbac.authorization.k8s.io/v1/clusterroles", role, kube.Object{"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "ClusterRole", "metadata": kube.Object{"name": role}, "rules": rules}); e != nil {
		return e
	}
	binding := func(kind, name string) kube.Object {
		return kube.Object{"apiVersion": "rbac.authorization.k8s.io/v1", "kind": kind, "metadata": kube.Object{"name": name, "namespace": c.Namespace}, "roleRef": kube.Object{"apiGroup": "rbac.authorization.k8s.io", "kind": "ClusterRole", "name": role}, "subjects": []any{kube.Object{"kind": "ServiceAccount", "name": "installer", "namespace": c.Namespace}}}
	}
	crb := binding("ClusterRoleBinding", role)
	delete(kube.Map(crb, "metadata"), "namespace")
	if e = applyOwned(ctx, k, c, "/apis/rbac.authorization.k8s.io/v1/clusterrolebindings", role, crb); e != nil {
		return e
	}
	// Resource creation cannot be restricted by resourceNames in Kubernetes RBAC.
	// Reads/deletes of bootstrap secrets are restricted to this connection's token.
	for _, namespace := range []string{c.Namespace, "kube-system"} {
		var r []any
		if namespace == "kube-system" {
			r = []any{
				kube.Object{"apiGroups": []string{""}, "resources": []string{"configmaps"}, "resourceNames": []string{"worker-config-k8nnect-" + c.Name + "-" + minorVersion(c.K0sVersion)}, "verbs": []string{"list", "watch"}},
				kube.Object{"apiGroups": []string{"rbac.authorization.k8s.io"}, "resources": []string{"roles", "rolebindings"}, "verbs": []string{"get", "create", "patch"}},
				kube.Object{"apiGroups": []string{""}, "resources": []string{"configmaps"}, "verbs": []string{"get", "create", "patch"}},
				kube.Object{"apiGroups": []string{""}, "resources": []string{"secrets"}, "verbs": []string{"create"}},
				kube.Object{"apiGroups": []string{""}, "resources": []string{"secrets"}, "resourceNames": []string{tokenName(c)}, "verbs": []string{"get", "delete"}},
			}
		} else {
			r = []any{
				kube.Object{"apiGroups": []string{""}, "resources": []string{"configmaps", "serviceaccounts", "services", "pods"}, "verbs": []string{"get", "list", "create", "patch", "delete"}},
				kube.Object{"apiGroups": []string{""}, "resources": []string{"pods/log"}, "verbs": []string{"get"}},
				kube.Object{"apiGroups": []string{""}, "resources": []string{"pods/exec"}, "verbs": []string{"get", "create"}},
				kube.Object{"apiGroups": []string{"apps"}, "resources": []string{"daemonsets"}, "verbs": []string{"get", "create", "patch"}},
				kube.Object{"apiGroups": []string{"batch"}, "resources": []string{"jobs"}, "verbs": []string{"get", "create", "delete"}},
				kube.Object{"apiGroups": []string{"rbac.authorization.k8s.io"}, "resources": []string{"roles", "rolebindings"}, "verbs": []string{"get", "create", "patch"}},
			}
		}
		name := role + "-local"
		path := "/apis/rbac.authorization.k8s.io/v1/namespaces/" + namespace
		if e = applyOwned(ctx, k, c, path+"/roles", name, kube.Object{"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "Role", "metadata": kube.Object{"name": name, "namespace": namespace}, "rules": r}); e != nil {
			return e
		}
		rb := kube.Object{"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "RoleBinding", "metadata": kube.Object{"name": name, "namespace": namespace}, "roleRef": kube.Object{"apiGroup": "rbac.authorization.k8s.io", "kind": "Role", "name": name}, "subjects": []any{kube.Object{"kind": "ServiceAccount", "name": "installer", "namespace": c.Namespace}}}
		if e = applyOwned(ctx, k, c, path+"/rolebindings", name, rb); e != nil {
			return e
		}
	}
	sources, e := payload(ctx, k, c)
	if e != nil {
		return e
	}
	labels := kube.Object{"app": "k8nnect-installer", "k8nnect.io/connection": c.Name}
	startup := "apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends ca-certificates && cat /payload/part* | gzip -d > /tmp/k8nnect && chmod 0700 /tmp/k8nnect && exec /tmp/k8nnect operator --config /connection/connection.json"
	pod := kube.Object{"serviceAccountName": "installer", "affinity": kube.Object{"nodeAffinity": kube.Object{"requiredDuringSchedulingIgnoredDuringExecution": kube.Object{"nodeSelectorTerms": []any{kube.Object{"matchExpressions": []any{kube.Object{"key": "k8nnect.io/external", "operator": "NotIn", "values": []string{"true"}}}}}}}}, "containers": []any{kube.Object{"name": "installer", "image": "ubuntu:24.04", "command": []string{"bash", "-ec", startup}, "securityContext": kube.Object{"allowPrivilegeEscalation": false}, "resources": kube.Object{"requests": kube.Object{"cpu": "25m", "memory": "32Mi"}, "limits": kube.Object{"memory": "256Mi"}}, "volumeMounts": []any{kube.Object{"name": "payload", "mountPath": "/payload", "readOnly": true}, kube.Object{"name": "connection", "mountPath": "/connection", "readOnly": true}, kube.Object{"name": "credentials", "mountPath": "/credentials", "readOnly": true}}}}, "volumes": []any{kube.Object{"name": "payload", "projected": kube.Object{"sources": sources}}, kube.Object{"name": "connection", "configMap": kube.Object{"name": "connection"}}, kube.Object{"name": "credentials", "secret": kube.Object{"secretName": "vds-ssh", "defaultMode": 256}}}}
	deployment := kube.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": kube.Object{"name": "installer", "namespace": c.Namespace}, "spec": kube.Object{"replicas": 1, "strategy": kube.Object{"type": "Recreate"}, "selector": kube.Object{"matchLabels": labels}, "template": kube.Object{"metadata": kube.Object{"labels": labels}, "spec": pod}}}
	if e = applyOwned(ctx, k, c, "/apis/apps/v1/namespaces/"+c.Namespace+"/deployments", "installer", deployment); e != nil {
		return e
	}
	fmt.Fprintln(out, "Addon installed; progress is reported in ConfigMap connection-status in", c.Namespace)
	return nil
}

func minorVersion(version string) string {
	parts := strings.Split(strings.TrimPrefix(version, "v"), ".")
	if len(parts) < 2 {
		return ""
	}
	return parts[0] + "." + parts[1]
}
func Operator(ctx context.Context, k *kube.Client, path string, out io.Writer) error {
	for {
		c, e := Load(path)
		if e != nil {
			return e
		}
		status := func(phase, detail string) {
			obj := kube.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": kube.Object{"name": "connection-status", "namespace": c.Namespace}, "data": kube.Object{"phase": phase, "node": c.NodeName, "detail": detail, "updatedAt": time.Now().UTC().Format(time.RFC3339)}}
			if err := applyOwned(ctx, k, c, "/api/v1/namespaces/"+c.Namespace+"/configmaps", "connection-status", obj); err != nil {
				fmt.Fprintln(out, "Status update failed:", err)
			}
		}
		status("Connecting", "")
		cycle, cancel := context.WithTimeout(ctx, 20*time.Minute)
		e = Connect(cycle, k, c, out)
		cancel()
		if e != nil {
			status("Error", e.Error())
			fmt.Fprintln(out, "Connection reconcile failed:", e)
		} else {
			status("Ready", "Worker connected and verified")
		}
		if e = pause(ctx, 60*time.Second); e != nil {
			return nil
		}
	}
}
