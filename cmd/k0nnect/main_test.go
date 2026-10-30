package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPlanRequiresNoPrivilegesAndPrintsNoSecrets(t *testing.T) {
	var out bytes.Buffer
	e := run(context.Background(), []string{"plan", "--config", "../../examples/inventory.json", "--node", "remote-worker"}, &out)
	if e != nil {
		t.Fatal(e)
	}
	for _, expected := range []string{"10.203.0.2/32", "10.203.0.1/32", `"podMTU": 1350`} {
		if !strings.Contains(out.String(), expected) {
			t.Fatalf("missing %s", expected)
		}
	}
	if strings.Contains(out.String(), "privateKey") || strings.Contains(out.String(), "PublicKey") {
		t.Fatal("plan leaked driver credential details")
	}
}
func TestJoinOutputIsPrivateAndNeverOverwritten(t *testing.T) {
	dir := t.TempDir()
	token := filepath.Join(dir, "token")
	output := filepath.Join(dir, "join.json")
	if e := os.WriteFile(token, []byte("abcdef.0123456789abcdef\n"), 0600); e != nil {
		t.Fatal(e)
	}
	args := []string{"join-config", "--config", "../../examples/inventory.json", "--node", "remote-worker", "--token-file", token, "--ca-hash", "sha256:" + strings.Repeat("a", 64), "--output", output}
	var out bytes.Buffer
	if e := run(context.Background(), args, &out); e != nil {
		t.Fatal(e)
	}
	info, e := os.Stat(output)
	if e != nil {
		t.Fatal(e)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatal("join credential is not private")
	}
	if strings.Contains(out.String(), "abcdef.0123456789abcdef") {
		t.Fatal("token leaked to stdout")
	}
	if e = run(context.Background(), args, &out); e == nil {
		t.Fatal("existing join file overwritten")
	}
	if e = os.Chmod(token, 0644); e != nil {
		t.Fatal(e)
	}
	if _, e = readToken(token); e == nil {
		t.Fatal("insecure token file accepted")
	}
}
