package enroll

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"time"

	"k0nnect/internal/kube"
)

// The in-cluster controller manages a single VDSConnection. Every object name
// it touches is fixed, so the static install manifest scopes its RBAC by name.
const (
	ControllerConnection = "vds"
	ControllerNode       = "k0nnect-vds"
	ControllerNamespace  = "k0nnect-system"
	SSHSecret            = "vds-ssh"
	finalizer            = "k0nnect.io/disconnect"
	connectionPath       = "/apis/k0nnect.io/v1alpha1/vdsconnections/" + ControllerConnection
)

// controllerConfig holds the fixed identity of the controller's connection.
func controllerConfig(image string) Config {
	return Config{Name: ControllerConnection, NodeName: ControllerNode, Namespace: ControllerNamespace, User: "root", Image: image, InCluster: true}
}

// connectionConfig builds the connection from the VDSConnection spec and the
// SSH Secret. Only network settings come from the spec; identity is fixed.
func connectionConfig(ctx context.Context, k *kube.Client, obj kube.Object, image string) (Config, error) {
	c := controllerConfig(image)
	b, e := json.Marshal(kube.Map(obj, "spec"))
	if e != nil {
		return c, e
	}
	var spec struct {
		Host          string `json:"host"`
		SSHPort       int    `json:"sshPort"`
		HostKeySHA256 string `json:"hostKeySHA256"`
		TransportCIDR string `json:"transportCIDR"`
		WorkerAddress string `json:"workerAddress"`
		Port          int    `json:"port"`
		MTU           int    `json:"mtu"`
		K0sVersion    string `json:"k0sVersion"`
	}
	if e = json.Unmarshal(b, &spec); e != nil {
		return c, e
	}
	c.Host, c.SSHPort, c.HostKeySHA256, c.TransportCIDR, c.WorkerAddress = spec.Host, spec.SSHPort, spec.HostKeySHA256, spec.TransportCIDR, spec.WorkerAddress
	c.Port, c.MTU, c.K0sVersion = spec.Port, spec.MTU, spec.K0sVersion
	if e = c.Validate(); e != nil {
		return c, e
	}
	secret, e := k.Get(ctx, "/api/v1/namespaces/"+c.Namespace+"/secrets/"+SSHSecret)
	if e != nil {
		return c, fmt.Errorf("read SSH Secret %s/%s: %w", c.Namespace, SSHSecret, e)
	}
	data := kube.Map(secret, "data")
	password, e := base64.StdEncoding.DecodeString(kube.Str(data, "password"))
	if e != nil {
		return c, e
	}
	if c.PrivateKey, e = base64.StdEncoding.DecodeString(kube.Str(data, "private-key")); e != nil {
		return c, e
	}
	c.Password = string(bytes.TrimSpace(password))
	if c.Password == "" && len(c.PrivateKey) == 0 {
		return c, fmt.Errorf("SSH Secret %s/%s needs a private-key or password key", c.Namespace, SSHSecret)
	}
	return c, nil
}

// Controller reconciles the VDSConnection: it connects the VDS on changes and
// every minute, and disconnects it before the object is deleted.
func Controller(ctx context.Context, k *kube.Client, image string, out io.Writer) error {
	if image == "" || !imageReference.MatchString(image) {
		return fmt.Errorf("controller requires its own image reference in --image")
	}
	observed, next := -1.0, time.Time{}
	for {
		obj, e := k.Get(ctx, connectionPath)
		switch {
		case kube.Code(e, 404):
			observed = -1
		case e != nil:
			fmt.Fprintln(out, "Read VDSConnection:", e)
		case kube.Str(kube.Map(obj, "metadata"), "deletionTimestamp") != "":
			if slices.Contains(finalizers(obj), finalizer) {
				disconnect(ctx, k, obj, image, out)
			}
		default:
			generation, _ := kube.Map(obj, "metadata")["generation"].(float64)
			if generation != observed || time.Now().After(next) {
				connect(ctx, k, obj, image, out)
				observed, next = generation, time.Now().Add(time.Minute)
			}
		}
		if pause(ctx, 10*time.Second) != nil {
			return nil
		}
	}
}

func finalizers(obj kube.Object) []string {
	var names []string
	for _, v := range kube.Items(kube.Map(obj, "metadata"), "finalizers") {
		if s, ok := v.(string); ok {
			names = append(names, s)
		}
	}
	return names
}

// editFinalizers rewrites the finalizer list of the current object; its
// resourceVersion guards against concurrent edits by others.
func editFinalizers(ctx context.Context, k *kube.Client, edit func([]string) []string) error {
	obj, e := k.Get(ctx, connectionPath)
	if e != nil {
		return e
	}
	_, e = k.Do(ctx, "PATCH", connectionPath, kube.Object{"metadata": kube.Object{"finalizers": edit(finalizers(obj)), "resourceVersion": kube.Str(kube.Map(obj, "metadata"), "resourceVersion")}})
	return e
}

func setStatus(ctx context.Context, k *kube.Client, obj kube.Object, phase, message string, out io.Writer) {
	generation, _ := kube.Map(obj, "metadata")["generation"].(float64)
	status := kube.Object{"phase": phase, "message": message, "node": ControllerNode, "observedGeneration": generation, "lastTransitionTime": time.Now().UTC().Format(time.RFC3339)}
	if _, e := k.Do(ctx, "PATCH", connectionPath+"/status", kube.Object{"status": status}); e != nil {
		fmt.Fprintln(out, "Status update failed:", e)
	}
}

func connect(ctx context.Context, k *kube.Client, obj kube.Object, image string, out io.Writer) {
	c, e := connectionConfig(ctx, k, obj, image)
	// Only a valid connection reaches the VDS, so only it needs a disconnect;
	// the immutable spec keeps the target it was provisioned with.
	if e == nil && !slices.Contains(finalizers(obj), finalizer) {
		e = editFinalizers(ctx, k, func(names []string) []string { return append(names, finalizer) })
	}
	if e == nil {
		setStatus(ctx, k, obj, "Connecting", "", out)
		cycle, cancel := context.WithTimeout(ctx, 20*time.Minute)
		e = Connect(cycle, k, c, out)
		cancel()
	}
	if e != nil {
		fmt.Fprintln(out, "Connection reconcile failed:", e)
		setStatus(ctx, k, obj, "Error", e.Error(), out)
		return
	}
	setStatus(ctx, k, obj, "Ready", "Worker connected and verified", out)
}

func disconnect(ctx context.Context, k *kube.Client, obj kube.Object, image string, out io.Writer) {
	setStatus(ctx, k, obj, "Disconnecting", "", out)
	c, e := connectionConfig(ctx, k, obj, image)
	if e != nil {
		// The fixed identity still names every cluster object; Disconnect
		// skips the VDS when nothing was started there.
		fmt.Fprintln(out, "Disconnecting without SSH credentials:", e)
	}
	cycle, cancel := context.WithTimeout(ctx, 20*time.Minute)
	e = Disconnect(cycle, k, c, true, out)
	cancel()
	if e == nil {
		e = editFinalizers(ctx, k, func(names []string) []string {
			return slices.DeleteFunc(names, func(s string) bool { return s == finalizer })
		})
	}
	if e != nil {
		fmt.Fprintln(out, "Disconnect failed:", e)
		setStatus(ctx, k, obj, "Error", "disconnect: "+e.Error(), out)
		return
	}
	fmt.Fprintln(out, "Disconnected; finalizer removed")
}
