package enroll

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"k0nnect/internal/kube"
)

func BootstrapDocument(server string, ca []byte, token string) (string, error) {
	doc := kube.Object{"apiVersion": "v1", "kind": "Config", "clusters": []any{kube.Object{"name": "k0s", "cluster": kube.Object{"server": server, "certificate-authority-data": base64.StdEncoding.EncodeToString(ca)}}}, "contexts": []any{kube.Object{"name": "k0s", "context": kube.Object{"cluster": "k0s", "user": "kubelet-bootstrap"}}}, "current-context": "k0s", "users": []any{kube.Object{"name": "kubelet-bootstrap", "user": kube.Object{"token": token}}}}
	b, e := json.Marshal(doc)
	if e != nil {
		return "", e
	}
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	if _, e = gz.Write(b); e != nil {
		return "", e
	}
	if e = gz.Close(); e != nil {
		return "", e
	}
	return base64.StdEncoding.EncodeToString(out.Bytes()), nil
}
func tokenName(c Config) string {
	sum := sha256.Sum256([]byte(c.Namespace + "/" + c.Name + "/" + c.NodeName))
	return "bootstrap-token-" + hex.EncodeToString(sum[:3])
}
func issueToken(ctx context.Context, k *kube.Client, c Config) (string, string, error) {
	b := make([]byte, 8)
	if _, e := rand.Read(b); e != nil {
		return "", "", e
	}
	name := tokenName(c)
	id := strings.TrimPrefix(name, "bootstrap-token-")
	secret := hex.EncodeToString(b)
	collection := "/api/v1/namespaces/kube-system/secrets"
	if old, err := k.Get(ctx, collection+"/"+name); err == nil {
		if kube.Str(kube.Map(kube.Map(old, "metadata"), "labels"), "k0nnect.io/connection") != c.Name {
			return "", "", fmt.Errorf("bootstrap token ID collides with a foreign token")
		}
		if err = k.Delete(ctx, collection+"/"+name); err != nil {
			return "", "", err
		}
	} else if !kube.Code(err, 404) {
		return "", "", err
	}
	obj := kube.Object{"apiVersion": "v1", "kind": "Secret", "metadata": kube.Object{"name": name, "namespace": "kube-system", "labels": kube.Object{"k0nnect.io/connection": c.Name}}, "type": "bootstrap.kubernetes.io/token", "stringData": kube.Object{"token-id": id, "token-secret": secret, "usage-bootstrap-authentication": "true", "description": "k0nnect short-lived k0s Worker enrollment", "expiration": time.Now().UTC().Add(time.Hour).Format(time.RFC3339)}}
	if _, e := k.Do(ctx, "POST", "/api/v1/namespaces/kube-system/secrets", obj); e != nil {
		return "", "", e
	}
	encoded, e := BootstrapDocument(k.Server, k.CA, id+"."+secret)
	return name, encoded, e
}

// Names of the cluster objects an installer manages; its RBAC is scoped to them.
func profileConfigMap(c Config, minor string) string {
	return "worker-config-k0nnect-" + c.Name + "-" + minor
}
func workerProfileRole(c Config) string { return c.Namespace + "-worker-profile" }
func gatewayRole(c Config) string       { return c.Namespace + "-gateway" }

func workerProfile(ctx context.Context, k *kube.Client, c Config, source kube.Object, minor string) (string, error) {
	name := "k0nnect-" + c.Name
	data := kube.Map(source, "data")
	copyData := kube.Object{}
	for key, v := range data {
		copyData[key] = v
	}
	var conf kube.Object
	if e := json.Unmarshal([]byte(kube.Str(data, "kubeletConfiguration")), &conf); e != nil {
		return "", e
	}
	// The provider's standalone containerd cgroup is absent on a fresh k0s worker.
	delete(conf, "kubeletCgroups")
	delete(conf, "kubeReservedCgroup")
	conf["resolvConf"] = "/etc/k0nnect/resolv.conf"
	b, e := json.Marshal(conf)
	if e != nil {
		return "", e
	}
	copyData["kubeletConfiguration"] = string(b)
	cm := profileConfigMap(c, minor)
	// k0s grants its generated profiles by explicit ConfigMap name. A cloned
	// profile needs its own bounded grant for bootstrap and the resulting node.
	rbac := "/apis/rbac.authorization.k8s.io/v1/namespaces/kube-system"
	roleName := workerProfileRole(c)
	role := kube.Object{"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "Role", "metadata": kube.Object{"name": roleName, "namespace": "kube-system"}, "rules": []any{kube.Object{"apiGroups": []string{""}, "resources": []string{"configmaps"}, "resourceNames": []string{cm}, "verbs": []string{"get", "list", "watch"}}}}
	if e := applyOwned(ctx, k, c, rbac+"/roles", roleName, role); e != nil {
		return "", e
	}
	subjects := []any{}
	for _, user := range []string{"system:node:" + c.NodeName, "system:bootstrap:" + strings.TrimPrefix(tokenName(c), "bootstrap-token-")} {
		subjects = append(subjects, kube.Object{"kind": "User", "apiGroup": "rbac.authorization.k8s.io", "name": user})
	}
	binding := kube.Object{"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "RoleBinding", "metadata": kube.Object{"name": roleName, "namespace": "kube-system"}, "roleRef": kube.Object{"apiGroup": "rbac.authorization.k8s.io", "kind": "Role", "name": roleName}, "subjects": subjects}
	if e := applyOwned(ctx, k, c, rbac+"/rolebindings", roleName, binding); e != nil {
		return "", e
	}
	obj := kube.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": kube.Object{"name": cm, "namespace": "kube-system", "labels": kube.Object{"k0s.k0sproject.io/worker-profile": name, "app.kubernetes.io/managed-by": "k0nnect", "k0nnect.io/connection": c.Name}}, "data": copyData}
	return name, applyOwned(ctx, k, c, "/api/v1/namespaces/kube-system/configmaps", cm, obj)
}
