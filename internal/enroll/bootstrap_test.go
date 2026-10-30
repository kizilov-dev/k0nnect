package enroll

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"io"
	"os/exec"
	"strings"
	"testing"
)

func TestBootstrapUsesWorkerIdentityAndPinnedCA(t *testing.T) {
	encoded, e := BootstrapDocument("https://example.test:6443", []byte("test CA"), "abcdef.0123456789abcdef")
	if e != nil {
		t.Fatal(e)
	}
	b, e := base64.StdEncoding.DecodeString(encoded)
	if e != nil {
		t.Fatal(e)
	}
	r, e := gzip.NewReader(bytes.NewReader(b))
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	b, e = io.ReadAll(r)
	if e != nil {
		t.Fatal(e)
	}
	var obj map[string]any
	if e = json.Unmarshal(b, &obj); e != nil {
		t.Fatal(e)
	}
	if obj["current-context"] != "k0s" || !strings.Contains(string(b), "\"user\":\"kubelet-bootstrap\"") || strings.Contains(string(b), "controller-bootstrap") {
		t.Fatal("wrong bootstrap identity")
	}
	if !strings.Contains(string(b), base64.StdEncoding.EncodeToString([]byte("test CA"))) {
		t.Fatal("CA missing")
	}
}
func TestShellQuotingIsLiteral(t *testing.T) {
	for _, value := range []string{"plain", "a'b", "$(echo unsafe) " + string(rune(96)) + "echo unsafe" + string(rune(96)), "x\n'y\";$PATH"} {
		b, e := exec.Command("sh", "-c", "printf %s "+Quote(value)).Output()
		if e != nil {
			t.Fatal(e)
		}
		if string(b) != value {
			t.Fatalf("shell quoting changed value: %q", b)
		}
	}
}
