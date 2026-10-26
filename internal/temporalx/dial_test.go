package temporalx

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDialRejectsAnEmptyAddress(t *testing.T) {
	if _, err := Dial(DialOptions{}); err == nil {
		t.Fatal("expected an error for an empty address")
	}
}

func TestPlainByDefault(t *testing.T) {
	cfg, err := tlsConfig(DialOptions{Address: "t:7233"})
	if err != nil || cfg != nil {
		t.Fatalf("want plain gRPC, got cfg=%v err=%v", cfg, err)
	}
}

func TestCAFileValidation(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(bad, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := tlsConfig(DialOptions{CAFile: bad}); err == nil {
		t.Fatal("a CA file without a certificate must be an error")
	}
	if _, err := tlsConfig(DialOptions{CAFile: filepath.Join(dir, "missing.pem")}); err == nil {
		t.Fatal("a missing CA file must be an error")
	}
}

func TestClientCertAndKeyComeTogether(t *testing.T) {
	if _, err := tlsConfig(DialOptions{CertFile: "c.pem"}); err == nil {
		t.Fatal("a certificate without a key must be an error")
	}
	if _, err := tlsConfig(DialOptions{KeyFile: "k.pem"}); err == nil {
		t.Fatal("a key without a certificate must be an error")
	}
}

func TestServerNameAloneTurnsOnVerifiedTLS(t *testing.T) {
	cfg, err := tlsConfig(DialOptions{ServerName: "temporal.internal"})
	if err != nil || cfg == nil || cfg.InsecureSkipVerify || cfg.ServerName != "temporal.internal" {
		t.Fatalf("got cfg=%+v err=%v", cfg, err)
	}
}

func TestLazyClientDoesNotConnect(t *testing.T) {
	c, err := Dial(DialOptions{Address: "127.0.0.1:1", Namespace: "default"})
	if err != nil {
		t.Fatalf("a lazy client must not fail while Temporal is down: %v", err)
	}
	c.Close()
}
