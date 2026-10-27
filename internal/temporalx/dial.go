package temporalx

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"

	"go.temporal.io/sdk/client"
)

// DialOptions uses plaintext by default; CAFile enables verified TLS and CertFile/KeyFile add mTLS.
type DialOptions struct {
	Address    string
	Namespace  string
	CAFile     string
	CertFile   string
	KeyFile    string
	ServerName string
	APIKey     string
	Identity   string
}

// Dial returns a lazy client so Temporal outages block pools without preventing startup probes.
func Dial(o DialOptions) (client.Client, error) {
	if o.Address == "" {
		return nil, errors.New("temporal address is empty")
	}
	tlsCfg, err := tlsConfig(o)
	if err != nil {
		return nil, err
	}
	opts := client.Options{HostPort: o.Address, Namespace: o.Namespace, Identity: o.Identity}
	if tlsCfg != nil {
		opts.ConnectionOptions = client.ConnectionOptions{TLS: tlsCfg}
	}
	if o.APIKey != "" {
		opts.Credentials = client.NewAPIKeyStaticCredentials(o.APIKey)
	}
	c, err := client.NewLazyClient(opts)
	if err != nil {
		return nil, fmt.Errorf("create temporal client: %w", err)
	}
	return c, nil
}

func tlsConfig(o DialOptions) (*tls.Config, error) {
	if o.CAFile == "" && o.CertFile == "" && o.KeyFile == "" && o.ServerName == "" {
		return nil, nil
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: o.ServerName}
	if o.CAFile != "" {
		pem, err := os.ReadFile(o.CAFile)
		if err != nil {
			return nil, fmt.Errorf("read temporal CA file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("temporal CA file %q holds no certificate", o.CAFile)
		}
		cfg.RootCAs = pool
	}
	if (o.CertFile == "") != (o.KeyFile == "") {
		return nil, errors.New("temporal client certificate and key must be set together")
	}
	if o.CertFile != "" {
		cert, err := tls.LoadX509KeyPair(o.CertFile, o.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("load temporal client certificate: %w", err)
		}
		cfg.Certificates = []tls.Certificate{cert}
	}
	return cfg, nil
}
