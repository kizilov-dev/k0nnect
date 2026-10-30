package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"k0nnect/internal/bootstrap/kubeadm"
	"k0nnect/internal/cni"
	"k0nnect/internal/enroll"
	"k0nnect/internal/kube"
	"k0nnect/internal/model"
	linux "k0nnect/internal/network/linux"
	"k0nnect/internal/planner"
	"k0nnect/internal/transport/wireguard"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "k0nnect:", err)
		os.Exit(1)
	}
}
func run(ctx context.Context, args []string, out io.Writer) error {
	if len(args) > 0 {
		switch args[0] {
		case "connect", "connect-plan", "connect-check", "verify-node", "disconnect", "gateway-agent", "gateway-remove", "worker-network", "worker-agent", "worker-remove", "operator", "addon-install":
			return enrollmentCommand(ctx, args, out)
		}
	}
	if len(args) == 0 {
		return fmt.Errorf("usage: k0nnect <connect-check|connect-plan|connect|addon-install|verify-node|disconnect> --config connection.json --kubeconfig PATH; static profile: <plan|doctor|apply|agent|status|verify|remove|join-config> --config inventory.json --node NAME")
	}
	f := flag.NewFlagSet(args[0], flag.ContinueOnError)
	config := f.String("config", "", "inventory JSON file")
	node := f.String("node", "", "local node name")
	interval := f.Duration("interval", 30*time.Second, "agent reconciliation interval (minimum 5s)")
	drained := f.Bool("drained", false, "acknowledge workloads have been drained before transport removal")
	tokenFile := f.String("token-file", "", "owner-only kubeadm bootstrap token file")
	caHash := f.String("ca-hash", "", "pinned discovery CA hash: sha256:...")
	output := f.String("output", "", "new owner-only join configuration file; never overwritten")
	flannelConfig := f.String("flannel-config", "", "optional local copy of Flannel net-conf.json to validate before any network changes")
	if err := f.Parse(args[1:]); err != nil {
		return err
	}
	if f.NArg() != 0 || *config == "" || *node == "" {
		return fmt.Errorf("--config and --node required; positional arguments are not accepted")
	}
	c, err := model.Load(*config)
	if err != nil {
		return err
	}
	n, err := c.Node(*node)
	if err != nil {
		return err
	}
	adapter, err := cni.ForConfig(c)
	if err != nil {
		return err
	}
	if *flannelConfig != "" {
		file, e := os.Open(*flannelConfig)
		if e != nil {
			return e
		}
		e = adapter.ValidateNetworkConfig(c, file)
		file.Close()
		if e != nil {
			return e
		}
	}
	encode := func(v any) error { enc := json.NewEncoder(out); enc.SetIndent("", "  "); return enc.Encode(v) }
	if args[0] == "plan" {
		p, e := planner.Build(c, *node)
		if e != nil {
			return e
		}
		return encode(p)
	}
	if args[0] == "join-config" {
		if *tokenFile == "" || *output == "" {
			return fmt.Errorf("--token-file, --ca-hash and --output required")
		}
		token, e := readToken(*tokenFile)
		if e != nil {
			return e
		}
		b, e := kubeadm.Render(c, *node, token, *caHash)
		if e != nil {
			return e
		}
		fd, e := syscall.Open(*output, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|syscall.O_NOFOLLOW, 0600)
		if e != nil {
			return e
		}
		file := os.NewFile(uintptr(fd), *output)
		_, e = file.Write(append(b, '\n'))
		e = errors.Join(e, file.Close())
		if e != nil {
			return e
		}
		fmt.Fprintln(out, "Worker JoinConfiguration written to", *output, "(contains a bootstrap credential)")
		return nil
	}
	switch args[0] {
	case "doctor", "apply", "agent", "status", "verify", "remove":
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
	if *interval < 5*time.Second {
		return fmt.Errorf("--interval must be at least 5s")
	}
	if os.Geteuid() != 0 {
		return fmt.Errorf("%s requires root to inspect/protect network state and keys; plan runs unprivileged", args[0])
	}
	unlock, err := lock()
	if err != nil {
		return err
	}
	defer unlock()
	d := &wireguard.Driver{Config: c, Node: n, Runner: linux.ExecRunner{}}
	switch args[0] {
	case "doctor":
		if err = d.Preflight(ctx); err != nil {
			return err
		}
		fmt.Fprintln(out, "Local transport preflight passed. CNI runtime, firewall, path MTU and Kubernetes readiness still require integration checks.")
		return nil
	case "apply":
		if err = d.Reconcile(ctx); err != nil {
			return err
		}
		fmt.Fprintln(out, "Transport configured; run verify and Kubernetes integration checks.")
		return nil
	case "status":
		status, e := d.Status(ctx)
		if e != nil {
			return e
		}
		return encode(status)
	case "verify":
		if err = d.Preflight(ctx); err != nil {
			return err
		}
		for _, peer := range c.Nodes {
			if peer.Name != n.Name {
				_, e := d.Runner.Run(ctx, "ping", []string{"-4", "-n", "-I", c.Transport.Interface, "-M", "do", "-s", strconv.Itoa(c.Transport.MTU - 28), "-c", "1", "-W", "3", peer.Address}, nil)
				if e != nil {
					return fmt.Errorf("MTU/connectivity probe to %s failed: %w", peer.Name, e)
				}
			}
		}
		status, e := d.Status(ctx)
		if e != nil {
			return e
		}
		for _, peer := range status {
			age := time.Now().Unix() - peer.LastHandshake
			if peer.LastHandshake == 0 || age < 0 || age > 180 {
				return fmt.Errorf("no recent authenticated handshake with %s", peer.Node)
			}
		}
		fmt.Fprintln(out, "Authenticated peer connectivity and transport MTU probes passed; Pod/CNI readiness is a separate check.")
		return nil
	case "remove":
		if !*drained {
			return fmt.Errorf("drain workloads and stop the agent, then pass --drained; removing the underlay disrupts Kubernetes")
		}
		if err = d.Remove(ctx); err != nil {
			return err
		}
		fmt.Fprintln(out, "Owned transport removed.")
		return nil
	case "agent":
		// Release the process lock between reconciliations so status and doctor work.
		unlock()
		ticker := time.NewTicker(*interval)
		defer ticker.Stop()
		for {
			if ctx.Err() != nil {
				return nil
			}
			cycleErr := func() error {
				release, e := lock()
				if e != nil {
					return e
				}
				defer release()
				fresh, e := model.Load(*config)
				if e != nil {
					return e
				}
				local, e := fresh.Node(*node)
				if e != nil {
					return e
				}
				if *flannelConfig != "" {
					adapter, e := cni.ForConfig(fresh)
					if e != nil {
						return e
					}
					file, e := os.Open(*flannelConfig)
					if e != nil {
						return e
					}
					e = adapter.ValidateNetworkConfig(fresh, file)
					file.Close()
					if e != nil {
						return e
					}
				}
				// Interface/address ownership migration is deliberately manual.
				if fresh.Cluster != c.Cluster || fresh.Transport != c.Transport || local.Address != n.Address || local.WireGuard.PrivateKeyFile != n.WireGuard.PrivateKeyFile {
					return fmt.Errorf("agent refuses transport identity/MTU migration; restart with a reviewed inventory")
				}
				return (&wireguard.Driver{Config: fresh, Node: local, Runner: d.Runner}).Reconcile(ctx)
			}()
			if cycleErr != nil {
				fmt.Fprintln(out, "Reconciliation failed:", cycleErr)
			} else {
				fmt.Fprintln(out, "Transport reconciled")
			}
			select {
			case <-ctx.Done():
				return nil
			case <-ticker.C:
			}
		}
	}
	return nil
}

func enrollmentCommand(ctx context.Context, args []string, out io.Writer) error {
	f := flag.NewFlagSet(args[0], flag.ContinueOnError)
	config := f.String("config", "", "connection JSON (or network JSON for worker agents)")
	kubeconfig := f.String("kubeconfig", "", "kubeconfig path; omitted uses in-cluster service account")
	output := f.String("output", "", "redacted review plan output file")
	drain := f.Bool("drain", false, "cordon and evict workloads before disconnect; honor disruption budgets")
	if e := f.Parse(args[1:]); e != nil {
		return e
	}
	if args[0] == "gateway-agent" {
		return enroll.Agent(ctx, "gateway-agent", "")
	}
	if *config == "" {
		return fmt.Errorf("--config required")
	}
	if args[0] == "worker-network" || args[0] == "worker-agent" {
		return enroll.Agent(ctx, args[0], *config)
	}
	if args[0] == "worker-remove" || args[0] == "gateway-remove" {
		return enroll.RemoveNetwork(ctx, args[0], *config)
	}
	c, e := enroll.Load(*config)
	if e != nil {
		return e
	}
	k, e := kube.New(ctx, *kubeconfig)
	if e != nil {
		return e
	}
	if args[0] == "connect" {
		return enroll.Connect(ctx, k, c, out)
	}
	switch args[0] {
	case "connect-plan":
		if *output == "" {
			return fmt.Errorf("--output required")
		}
		return enroll.Preview(ctx, k, c, *output, out)
	case "connect-check":
		return enroll.Check(ctx, k, c, out)
	case "verify-node":
		return enroll.VerifyNode(ctx, k, c, out)
	case "disconnect":
		return enroll.Disconnect(ctx, k, c, *drain, out)
	case "addon-install":
		return enroll.InstallAddon(ctx, k, c, out)
	case "operator":
		return enroll.Operator(ctx, k, *config, out)
	}
	return fmt.Errorf("unknown enrollment command")
}

func lock() (func(), error) {
	const dir = "/run/k0nnect"
	if err := os.Mkdir(dir, 0700); err != nil && !os.IsExist(err) {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode().Perm()&0077 != 0 || stat.Uid != uint32(os.Geteuid()) {
		return nil, fmt.Errorf("unsafe lock directory %s", dir)
	}
	fd, err := syscall.Open(dir+"/network.lock", syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "network.lock")
	if err = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		return nil, fmt.Errorf("another k0nnect operation is active: %w", err)
	}
	closed := false
	return func() {
		if !closed {
			closed = true
			file.Close()
		}
	}, nil
}
func readToken(path string) (string, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || stat.Uid != uint32(os.Geteuid()) {
		return "", fmt.Errorf("bootstrap token must be a regular owner-only file owned by the executing user")
	}
	b, err := io.ReadAll(io.LimitReader(f, 128))
	return strings.TrimSpace(string(b)), err
}
