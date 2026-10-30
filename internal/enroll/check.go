package enroll

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"k8nnect/internal/kube"
)

// Check authenticates and reads state only; it never installs or configures.
func Check(ctx context.Context, k *kube.Client, c Config, out io.Writer) error {
	if e := c.Validate(); e != nil {
		return e
	}
	peers, source, minor, e := discover(ctx, k, &c)
	if e != nil {
		return e
	}
	r, e := Dial(ctx, c)
	if e != nil {
		return e
	}
	defer r.Close()
	script := "import json,os,platform,shutil; print(json.dumps({'uid':os.getuid(),'os':platform.system(),'architecture':platform.machine(),'aptAvailable':shutil.which('apt-get') is not None,'managedInstallation':os.path.isfile('/etc/k8nnect/managed.json'),'workerNamespaceExists':os.path.exists('/run/netns/k8nnect'),'k0sInstalled':shutil.which('k0s') is not None}))"
	b, e := r.Run(ctx, "python3 -c "+Quote(script), nil)
	if e != nil {
		return e
	}
	var state kube.Object
	if e = json.Unmarshal(b, &state); e != nil {
		return e
	}
	result := kube.Object{"readOnly": true, "sshHostKeyVerified": true, "vds": state, "gatewayCandidates": peers, "kubernetesMinor": minor, "k0sVersion": c.K0sVersion, "cni": c.CNI, "controllerAddresses": kube.Str(kube.Map(source, "data"), "apiServerAddresses")}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	if e = enc.Encode(result); e != nil {
		return e
	}
	fmt.Fprintln(out, "No changes applied.")
	return nil
}
