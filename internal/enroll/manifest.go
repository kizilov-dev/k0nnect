package enroll

import (
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"slices"
	"sort"
	"strings"

	"k0nnect/internal/kube"
)

// ControllerManifest returns the objects of the install manifest. RBAC
// creation cannot be restricted by name in Kubernetes; every other cluster-wide
// write and every read outside discovery is limited to the fixed object names
// of the controller's connection.
func ControllerManifest(image string) ([]kube.Object, error) {
	if !imageReference.MatchString(image) {
		return nil, fmt.Errorf("invalid image reference")
	}
	c := controllerConfig(image)
	labels := kube.Object{"app.kubernetes.io/name": "k0nnect", "app.kubernetes.io/component": "controller"}
	meta := func(name, namespace string) kube.Object {
		m := kube.Object{"name": name, "labels": labels}
		if namespace != "" {
			m["namespace"] = namespace
		}
		return m
	}
	rule := func(group string, resources, names, verbs []string) kube.Object {
		r := kube.Object{"apiGroups": []string{group}, "resources": resources, "verbs": verbs}
		if len(names) > 0 {
			r["resourceNames"] = names
		}
		return r
	}
	var cniDaemonSetNames, profiles, defaults []string
	for _, d := range cniDaemonSets {
		if !slices.Contains(cniDaemonSetNames, d.daemonSet) {
			cniDaemonSetNames = append(cniDaemonSetNames, d.daemonSet)
		}
	}
	for _, minor := range supportedMinors {
		profiles = append(profiles, profileConfigMap(c, minor))
		defaults = append(defaults, "worker-config-default-"+minor)
	}
	subject := []any{kube.Object{"kind": "ServiceAccount", "name": "controller", "namespace": c.Namespace}}
	binding := func(kind, roleKind, namespace string) kube.Object {
		return kube.Object{"apiVersion": "rbac.authorization.k8s.io/v1", "kind": kind, "metadata": meta("k0nnect-controller", namespace), "roleRef": kube.Object{"apiGroup": "rbac.authorization.k8s.io", "kind": roleKind, "name": "k0nnect-controller"}, "subjects": subject}
	}
	clusterRules := []any{
		rule("k0nnect.io", []string{"vdsconnections"}, []string{c.Name}, []string{"get", "patch"}),
		rule("k0nnect.io", []string{"vdsconnections/status"}, []string{c.Name}, []string{"get", "patch"}),
		rule("", []string{"nodes"}, nil, []string{"get", "list"}),
		rule("", []string{"nodes"}, []string{c.NodeName}, []string{"patch", "delete"}),
		rule("", []string{"namespaces", "services"}, nil, []string{"get", "list"}),
		// Draining the external node before disconnect.
		rule("", []string{"pods"}, nil, []string{"list"}),
		rule("", []string{"pods/eviction"}, nil, []string{"create"}),
		rule("apps", []string{"daemonsets"}, cniDaemonSetNames, []string{"get"}),
		rule("crd.projectcalico.org", []string{"ippools"}, nil, []string{"get", "list"}),
		rule("", []string{"configmaps"}, cniConfigMaps, []string{"get"}),
		rule("certificates.k8s.io", []string{"certificatesigningrequests"}, nil, []string{"get", "list"}),
		rule("certificates.k8s.io", []string{"certificatesigningrequests/approval"}, nil, []string{"update"}),
		rule("certificates.k8s.io", []string{"signers"}, []string{"kubernetes.io/kubelet-serving"}, []string{"approve"}),
		rule("rbac.authorization.k8s.io", []string{"clusterroles", "clusterrolebindings"}, nil, []string{"create"}),
		rule("rbac.authorization.k8s.io", []string{"clusterroles", "clusterrolebindings"}, []string{gatewayRole(c)}, []string{"get", "patch", "delete"}),
	}
	systemRules := []any{
		rule("", []string{"configmaps"}, profiles, []string{"get", "list", "watch", "patch", "delete"}),
		rule("", []string{"configmaps"}, defaults, []string{"get"}),
		rule("", []string{"configmaps"}, nil, []string{"create"}),
		rule("rbac.authorization.k8s.io", []string{"roles", "rolebindings"}, nil, []string{"create"}),
		rule("rbac.authorization.k8s.io", []string{"roles", "rolebindings"}, []string{workerProfileRole(c)}, []string{"get", "patch", "delete"}),
		rule("", []string{"secrets"}, nil, []string{"create"}),
		rule("", []string{"secrets"}, []string{tokenName(c)}, []string{"get", "delete"}),
	}
	localRules := []any{
		rule("", []string{"secrets"}, []string{SSHSecret}, []string{"get"}),
		rule("", []string{"configmaps", "serviceaccounts", "services", "pods"}, nil, []string{"get", "list", "create", "patch", "delete"}),
		rule("", []string{"pods/log"}, nil, []string{"get"}),
		rule("", []string{"pods/exec"}, nil, []string{"get", "create"}),
		rule("apps", []string{"daemonsets"}, nil, []string{"get", "list", "create", "patch", "delete"}),
		rule("batch", []string{"jobs"}, nil, []string{"get", "list", "create", "delete"}),
		rule("rbac.authorization.k8s.io", []string{"roles", "rolebindings"}, nil, []string{"get", "list", "create", "patch", "delete"}),
	}
	podLabels := kube.Object{"app.kubernetes.io/name": "k0nnect", "app.kubernetes.io/component": "controller"}
	container := kube.Object{
		"name": "controller", "image": image, "command": []string{"/usr/local/bin/k0nnect", "controller", "--image", image},
		"securityContext": kube.Object{"runAsNonRoot": true, "runAsUser": 65532, "runAsGroup": 65532, "allowPrivilegeEscalation": false, "readOnlyRootFilesystem": true, "capabilities": kube.Object{"drop": []string{"ALL"}}, "seccompProfile": kube.Object{"type": "RuntimeDefault"}},
		"resources":       kube.Object{"requests": kube.Object{"cpu": "25m", "memory": "64Mi"}, "limits": kube.Object{"memory": "256Mi"}},
	}
	// The controller never runs on the VDS it manages.
	affinity := kube.Object{"nodeAffinity": kube.Object{"requiredDuringSchedulingIgnoredDuringExecution": kube.Object{"nodeSelectorTerms": []any{kube.Object{"matchExpressions": []any{kube.Object{"key": "k0nnect.io/external", "operator": "NotIn", "values": []string{"true"}}}}}}}}
	namespaceMeta := meta(c.Namespace, "")
	namespaceMeta["labels"] = kube.Object{"app.kubernetes.io/name": "k0nnect", "app.kubernetes.io/managed-by": "k0nnect", "pod-security.kubernetes.io/enforce": "privileged"}
	namespaceMeta["annotations"] = kube.Object{"k0nnect.io/controller": c.Name}
	return []kube.Object{
		{"apiVersion": "v1", "kind": "Namespace", "metadata": namespaceMeta},
		connectionCRD(),
		{"apiVersion": "v1", "kind": "ServiceAccount", "metadata": meta("controller", c.Namespace)},
		{"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "ClusterRole", "metadata": meta("k0nnect-controller", ""), "rules": clusterRules},
		binding("ClusterRoleBinding", "ClusterRole", ""),
		{"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "Role", "metadata": meta("k0nnect-controller", "kube-system"), "rules": systemRules},
		binding("RoleBinding", "Role", "kube-system"),
		{"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "Role", "metadata": meta("k0nnect-controller", c.Namespace), "rules": localRules},
		binding("RoleBinding", "Role", c.Namespace),
		{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": meta("controller", c.Namespace), "spec": kube.Object{
			"replicas": 1, "strategy": kube.Object{"type": "Recreate"}, "selector": kube.Object{"matchLabels": podLabels},
			"template": kube.Object{"metadata": kube.Object{"labels": podLabels}, "spec": kube.Object{"serviceAccountName": "controller", "affinity": affinity, "containers": []any{container}}},
		}},
	}, nil
}

func connectionCRD() kube.Object {
	str := func(description string) kube.Object { return kube.Object{"type": "string", "description": description} }
	pattern := func(description, re string) kube.Object {
		return kube.Object{"type": "string", "description": description, "pattern": re}
	}
	ipv4 := `^([0-9]{1,3}\.){3}[0-9]{1,3}$`
	integer := func(description string, def, min, max int) kube.Object {
		return kube.Object{"type": "integer", "description": description, "default": def, "minimum": min, "maximum": max}
	}
	spec := kube.Object{"type": "object", "required": []string{"host", "hostKeySHA256", "transportCIDR", "workerAddress"}, "properties": kube.Object{
		"host":          pattern("Public IPv4 address of the VDS.", ipv4),
		"sshPort":       integer("SSH port of the VDS.", 22, 1, 65535),
		"hostKeySHA256": pattern("Pinned SHA256 fingerprint of the VDS Ed25519 SSH host key, e.g. SHA256:...", `^SHA256:[A-Za-z0-9+/]{43}=?$`),
		"transportCIDR": pattern("Private IPv4 /24 for the WireGuard transport; must not overlap node, Pod or Service addresses.", `^([0-9]{1,3}\.){3}0/24$`),
		"workerAddress": pattern("Worker address in transportCIDR; last octet 100..254.", ipv4),
		"port":          integer("UDP NodePort of the gateways; the VDS listens on port+1.", 31820, 30000, 32766),
		"mtu":           integer("WireGuard transport MTU.", 1400, 1280, 1440),
		"k0sVersion":    pattern("k0s release for the VDS, e.g. v1.36.4+k0s.0; defaults to the cluster's release.", versionPattern.String()),
	},
		// Disconnect must reach the VDS the Worker was provisioned on.
		"x-kubernetes-validations": []any{kube.Object{"rule": "self == oldSelf", "message": "spec is immutable; delete the VDSConnection and create a new one"}}}
	status := kube.Object{"type": "object", "properties": kube.Object{
		"phase":              str("Connecting, Ready, Disconnecting or Error."),
		"message":            str("Last error or result."),
		"node":               str("Name of the Kubernetes Node of the VDS."),
		"observedGeneration": kube.Object{"type": "integer"},
		"lastTransitionTime": kube.Object{"type": "string", "format": "date-time"},
	}}
	schema := kube.Object{"type": "object", "properties": kube.Object{"spec": spec, "status": status},
		"x-kubernetes-validations": []any{kube.Object{"rule": "self.metadata.name == '" + ControllerConnection + "'", "message": "the controller manages one VDSConnection named " + ControllerConnection}}}
	column := func(name, path string) kube.Object {
		return kube.Object{"name": name, "type": "string", "jsonPath": path}
	}
	return kube.Object{"apiVersion": "apiextensions.k8s.io/v1", "kind": "CustomResourceDefinition",
		"metadata": kube.Object{"name": "vdsconnections.k0nnect.io", "labels": kube.Object{"app.kubernetes.io/name": "k0nnect"}},
		"spec": kube.Object{"group": "k0nnect.io", "scope": "Cluster",
			"names": kube.Object{"kind": "VDSConnection", "listKind": "VDSConnectionList", "plural": "vdsconnections", "singular": "vdsconnection", "shortNames": []string{"vdsc"}},
			"versions": []any{kube.Object{"name": "v1alpha1", "served": true, "storage": true, "subresources": kube.Object{"status": kube.Object{}},
				"additionalPrinterColumns": []any{column("Phase", ".status.phase"), column("Node", ".status.node"), column("Host", ".spec.host"), column("Message", ".status.message"), kube.Object{"name": "Age", "type": "date", "jsonPath": ".metadata.creationTimestamp"}},
				"schema":                   kube.Object{"openAPIV3Schema": schema}}}}}
}

// WriteYAML writes objects as a multi-document YAML stream. Strings are
// double-quoted JSON strings, which YAML reads unchanged.
func WriteYAML(w io.Writer, objects []kube.Object) error {
	var b strings.Builder
	for _, obj := range objects {
		raw, e := json.Marshal(obj)
		if e != nil {
			return e
		}
		var v any
		if e = json.Unmarshal(raw, &v); e != nil {
			return e
		}
		b.WriteString("---\n")
		writeYAML(&b, v, 0)
	}
	_, e := io.WriteString(w, b.String())
	return e
}

var plainKey = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)

func writeYAML(b *strings.Builder, v any, indent int) {
	pad := strings.Repeat("  ", indent)
	key := func(k string) string {
		if plainKey.MatchString(k) {
			return k
		}
		q, _ := json.Marshal(k)
		return string(q)
	}
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			b.WriteString(pad + key(k) + ":")
			writeValue(b, t[k], indent)
		}
	case []any:
		for _, item := range t {
			b.WriteString(pad + "-")
			if m, ok := item.(map[string]any); ok && len(m) > 0 {
				// The first key shares the dash line; the rest align under it.
				var inner strings.Builder
				writeYAML(&inner, m, indent+1)
				b.WriteString(" " + strings.TrimPrefix(inner.String(), pad+"  "))
				continue
			}
			writeValue(b, item, indent)
		}
	}
}

func writeValue(b *strings.Builder, v any, indent int) {
	switch t := v.(type) {
	case map[string]any:
		if len(t) == 0 {
			b.WriteString(" {}\n")
			return
		}
		b.WriteString("\n")
		writeYAML(b, t, indent+1)
	case []any:
		if len(t) == 0 {
			b.WriteString(" []\n")
			return
		}
		b.WriteString("\n")
		writeYAML(b, t, indent+1)
	default:
		scalar, _ := json.Marshal(t)
		b.WriteString(" " + string(scalar) + "\n")
	}
}
