package enroll

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"k0nnect/internal/kube"
)

const verifyServer = `import http.server,socket,threading
payload=bytes(i%251 for i in range(65536))
class H(http.server.BaseHTTPRequestHandler):
 def do_GET(self):
  self.send_response(200);self.send_header('Content-Length',str(len(payload)));self.end_headers();self.wfile.write(payload)
 def log_message(self,*args): print('http request',flush=True)
def udp():
 s=socket.socket(socket.AF_INET,socket.SOCK_DGRAM);s.bind(('0.0.0.0',8081))
 while True:
  data,peer=s.recvfrom(65535);s.sendto(data,peer)
threading.Thread(target=udp,daemon=True).start()
print('server ready',flush=True)
http.server.ThreadingHTTPServer(('0.0.0.0',8080),H).serve_forever()
`
const verifyClient = `import socket,sys,urllib.request
payload=bytes(i%251 for i in range(65536))
for host in sys.argv[1:]:
 print('TCP probe',host,flush=True)
 data=urllib.request.urlopen('http://'+host+':8080/payload',timeout=15).read()
 assert data==payload,'TCP/Service payload mismatch'
s=socket.socket(socket.AF_INET,socket.SOCK_DGRAM);s.settimeout(5)
for size in [1000,1422,8192]:
 print('UDP probe',size,sys.argv[1],flush=True)
 # A DF datagram above a smaller path MTU is dropped once while the kernel
 # learns that MTU from ICMP; the next one is fragmented by the sender.
 for attempt in range(3):
  s.sendto(payload[:size],(sys.argv[1],8081))
  try:
   data,_=s.recvfrom(65535);break
  except socket.timeout:
   if attempt==2: raise
   print('UDP retry after path MTU discovery',size,flush=True)
 assert data==payload[:size],'UDP fragmentation payload mismatch'
print('PASS TCP 65536 bytes, Service DNS, UDP 1000/1422/8192 bytes',flush=True)
`

func ownedDelete(ctx context.Context, k *kube.Client, path, owner string) error {
	obj, e := k.Get(ctx, path)
	if kube.Code(e, 404) {
		return nil
	}
	if e != nil {
		return e
	}
	if kube.Str(kube.Map(kube.Map(obj, "metadata"), "labels"), "k0nnect.io/connection") != owner {
		return fmt.Errorf("refusing to delete foreign object %s", path)
	}
	return k.Delete(ctx, path)
}
func VerifyNode(ctx context.Context, k *kube.Client, c Config, out io.Writer) error {
	nodes, err := k.Get(ctx, "/api/v1/nodes")
	if err != nil {
		return err
	}
	// A CNI may program a just-joined node on existing nodes asynchronously.
	// Failures inside this window are retried; later ones are final.
	settled := time.Now().Add(cniSettleTime(ctx, k))
	checked := 0
	for _, v := range kube.Items(nodes, "items") {
		node := kube.As(v)
		if !nodesReady(node) || kube.Str(kube.Map(kube.Map(node, "metadata"), "labels"), "k0nnect.io/external") == "true" {
			continue
		}
		internal := kube.Str(kube.Map(node, "metadata"), "name")
		fmt.Fprintln(out, "Verifying external Worker against", internal)
		for {
			if err = verifyNodePair(ctx, k, c, internal, out); err == nil || time.Now().After(settled) {
				break
			}
			fmt.Fprintln(out, "Verification failed while the CNI may still be converging; retrying:", err)
			if err = pause(ctx, 30*time.Second); err != nil {
				return err
			}
		}
		if err != nil {
			return err
		}
		checked++
	}
	if checked == 0 {
		return fmt.Errorf("no existing worker for cross-node verification")
	}
	return activateNode(ctx, k, c, out)
}

func verifyNodePair(ctx context.Context, k *kube.Client, c Config, internal string, out io.Writer) (result error) {
	fmt.Fprintln(out, "Scheduling verification workloads on the external Worker")
	ns := "/api/v1/namespaces/" + c.Namespace
	var e error
	// Fresh Service DNS names avoid CoreDNS caching a previous verification's
	// ClusterIP after that Service was deleted and recreated between node pairs.
	suffix := strings.ToLower(rand.Text()[:10])
	serviceNames := map[string]string{"external": "verify-external-" + suffix, "internal": "verify-internal-" + suffix}
	labels := func(name string) kube.Object { return kube.Object{"app": name, "k0nnect.io/connection": c.Name} }
	paths := []string{ns + "/pods/verify-external", ns + "/pods/verify-internal", ns + "/services/" + serviceNames["external"], ns + "/services/" + serviceNames["internal"], "/apis/batch/v1/namespaces/" + c.Namespace + "/jobs/verify-to-external", "/apis/batch/v1/namespaces/" + c.Namespace + "/jobs/verify-from-external"}
	services, e := k.Get(ctx, ns+"/services?labelSelector=k0nnect.io%2Fconnection%3D"+c.Name)
	if e != nil {
		return e
	}
	for _, value := range kube.Items(services, "items") {
		meta := kube.Map(kube.As(value), "metadata")
		app := kube.Str(kube.Map(meta, "labels"), "app")
		if app == "verify-external" || app == "verify-internal" {
			paths = append(paths, ns+"/services/"+kube.Str(meta, "name"))
		}
	}
	for _, path := range paths {
		if e = ownedDelete(ctx, k, path, c.Name); e != nil {
			return e
		}
	}
	defer func() {
		if result != nil {
			fmt.Fprintln(out, "Verification resources retained for diagnosis; the next verification removes them")
			return
		}
		cleanup, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		for _, path := range paths {
			if err := ownedDelete(cleanup, k, path, c.Name); err != nil && result == nil {
				result = err
			}
		}
	}()
	// Wait for deletion before recreating immutable Pod/Job specs.
	for _, path := range paths {
		for {
			_, err := k.Get(ctx, path)
			if kube.Code(err, 404) {
				break
			}
			if err != nil {
				return err
			}
			if e = pause(ctx, time.Second); e != nil {
				return e
			}
		}
	}
	tolerance := []any{kube.Object{"key": "k0nnect.io/initializing", "operator": "Exists", "effect": "NoSchedule"}}
	placement := func(external bool) kube.Object {
		spec := kube.Object{"restartPolicy": "Never", "terminationGracePeriodSeconds": 1, "automountServiceAccountToken": false, "tolerations": tolerance}
		if external {
			spec["nodeSelector"] = kube.Object{"k0nnect.io/external": "true", "k0nnect.io/connection": c.Name}
		} else {
			spec["nodeName"] = internal
		}
		return spec
	}
	for _, side := range []string{"external", "internal"} {
		name := "verify-" + side
		spec := placement(side == "external")
		spec["containers"] = []any{kube.Object{"name": "server", "image": "python:3.13-alpine", "command": []string{"python", "-u", "-c", verifyServer}, "resources": kube.Object{"requests": kube.Object{"memory": "24Mi", "cpu": "10m"}, "limits": kube.Object{"memory": "96Mi"}}, "readinessProbe": kube.Object{"tcpSocket": kube.Object{"port": 8080}, "periodSeconds": 2}}}
		obj := kube.Object{"apiVersion": "v1", "kind": "Pod", "metadata": kube.Object{"name": name, "namespace": c.Namespace, "labels": labels(name)}, "spec": spec}
		if _, e = k.Do(ctx, "POST", ns+"/pods", obj); e != nil {
			return e
		}
		svc := kube.Object{"apiVersion": "v1", "kind": "Service", "metadata": kube.Object{"name": serviceNames[side], "namespace": c.Namespace, "labels": labels(name)}, "spec": kube.Object{"selector": labels(name), "ports": []any{kube.Object{"port": 8080, "targetPort": 8080}}}}
		if _, e = k.Do(ctx, "POST", ns+"/services", svc); e != nil {
			return e
		}
	}
	ips := map[string]string{}
	deadline := time.Now().Add(4 * time.Minute)
	for _, side := range []string{"external", "internal"} {
		for {
			p, err := k.Get(ctx, ns+"/pods/verify-"+side)
			if err != nil {
				return err
			}
			ready := false
			for _, v := range kube.Items(kube.Map(p, "status"), "conditions") {
				cond := kube.As(v)
				if kube.Str(cond, "type") == "Ready" && kube.Str(cond, "status") == "True" {
					ready = true
				}
			}
			if ready {
				ips[side] = kube.Str(kube.Map(p, "status"), "podIP")
				if side == "external" && kube.Str(kube.Map(p, "spec"), "nodeName") != c.NodeName {
					return fmt.Errorf("scheduler selected an unexpected node")
				}
				break
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("verification server Pod not Ready")
			}
			if e = pause(ctx, 3*time.Second); e != nil {
				return e
			}
		}
	}
	for _, direction := range []string{"to-external", "from-external"} {
		target := "external"
		if direction == "from-external" {
			target = "internal"
		}
		name := "verify-" + direction
		spec := placement(direction == "from-external")
		spec["containers"] = []any{kube.Object{"name": "probe", "image": "python:3.13-alpine", "command": []string{"python", "-u", "-c", verifyClient, ips[target], serviceNames[target]}, "resources": kube.Object{"requests": kube.Object{"memory": "24Mi", "cpu": "10m"}, "limits": kube.Object{"memory": "96Mi"}}}}
		job := kube.Object{"apiVersion": "batch/v1", "kind": "Job", "metadata": kube.Object{"name": name, "namespace": c.Namespace, "labels": labels(name)}, "spec": kube.Object{"backoffLimit": 0, "activeDeadlineSeconds": 120, "ttlSecondsAfterFinished": 300, "template": kube.Object{"metadata": kube.Object{"labels": labels(name)}, "spec": spec}}}
		if _, e = k.Do(ctx, "POST", "/apis/batch/v1/namespaces/"+c.Namespace+"/jobs", job); e != nil {
			return e
		}
	}
	for _, direction := range []string{"to-external", "from-external"} {
		for {
			job, err := k.Get(ctx, "/apis/batch/v1/namespaces/"+c.Namespace+"/jobs/verify-"+direction)
			if err != nil {
				return err
			}
			status := kube.Map(job, "status")
			if success, ok := status["succeeded"].(float64); ok && success > 0 {
				break
			}
			if failed, ok := status["failed"].(float64); ok && failed > 0 {
				pods, _ := k.Get(ctx, ns+"/pods?labelSelector=job-name%3Dverify-"+direction)
				for _, value := range kube.Items(pods, "items") {
					name := kube.Str(kube.Map(kube.As(value), "metadata"), "name")
					if logs, err := k.Raw(ctx, ns+"/pods/"+name+"/log?tailLines=60"); err == nil {
						fmt.Fprintln(out, string(logs))
					}
				}
				return fmt.Errorf("%s TCP/UDP/DNS verification Job failed", direction)
			}
			if e = pause(ctx, 3*time.Second); e != nil {
				return e
			}
		}
	}
	if e = approveServing(ctx, k, c); e != nil {
		return e
	}
	if _, e = k.Raw(ctx, ns+"/pods/verify-external/log?container=server&tailLines=5"); e != nil {
		return fmt.Errorf("external kubelet logs verification: %w", e)
	}
	if e = k.ExecTrue(ctx, c.Namespace, "verify-external"); e != nil {
		return e
	}
	return nil
}

func activateNode(ctx context.Context, k *kube.Client, c Config, out io.Writer) error {
	n, e := k.Get(ctx, "/api/v1/nodes/"+url.PathEscape(c.NodeName))
	if e != nil {
		return e
	}
	taints := []any{}
	for _, v := range kube.Items(kube.Map(n, "spec"), "taints") {
		if kube.Str(kube.As(v), "key") != "k0nnect.io/initializing" {
			taints = append(taints, v)
		}
	}
	_, e = k.Do(ctx, "PATCH", "/api/v1/nodes/"+c.NodeName, kube.Object{"metadata": kube.Object{"annotations": kube.Object{"k0nnect.io/verified": time.Now().UTC().Format(time.RFC3339)}}, "spec": kube.Object{"taints": taints}})
	if e == nil {
		fmt.Fprintln(out, "PASS: scheduler, bidirectional TCP/UDP, large packets, Service DNS, logs and exec; Worker activated")
	}
	return e
}
