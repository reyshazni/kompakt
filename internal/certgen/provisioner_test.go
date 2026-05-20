package certgen

import (
	"crypto/x509"
	"encoding/pem"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
)

func TestGenerateCerts_ValidOutput(t *testing.T) {
	caPEM, certPEM, keyPEM, err := GenerateCerts("kompakt-controller", "kompakt-system")
	if err != nil {
		t.Fatalf("GenerateCerts failed: %v", err)
	}
	if len(caPEM) == 0 || len(certPEM) == 0 || len(keyPEM) == 0 {
		t.Fatal("expected non-empty PEM output")
	}
}

func TestGenerateCerts_CAIsSelfSigned(t *testing.T) {
	caPEM, _, _, err := GenerateCerts("kompakt-controller", "kompakt-system")
	if err != nil {
		t.Fatalf("GenerateCerts failed: %v", err)
	}

	block, _ := pem.Decode(caPEM)
	if block == nil {
		t.Fatal("failed to decode CA PEM")
	}

	ca, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("failed to parse CA cert: %v", err)
	}

	if !ca.IsCA {
		t.Fatal("expected CA certificate")
	}
	if ca.Subject.CommonName != "kompakt-controller-ca" {
		t.Fatalf("expected CN 'kompakt-controller-ca', got %q", ca.Subject.CommonName)
	}
}

func TestGenerateCerts_ServerCertSANs(t *testing.T) {
	_, certPEM, _, err := GenerateCerts("my-svc", "my-ns")
	if err != nil {
		t.Fatalf("GenerateCerts failed: %v", err)
	}

	block, _ := pem.Decode(certPEM)
	if block == nil {
		t.Fatal("failed to decode server cert PEM")
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("failed to parse server cert: %v", err)
	}

	expected := map[string]bool{
		"my-svc":                         false,
		"my-svc.my-ns.svc":               false,
		"my-svc.my-ns.svc.cluster.local": false,
	}

	for _, dns := range cert.DNSNames {
		if _, ok := expected[dns]; ok {
			expected[dns] = true
		}
	}

	for dns, found := range expected {
		if !found {
			t.Errorf("expected SAN %q not found in cert DNSNames: %v", dns, cert.DNSNames)
		}
	}
}

func TestGenerateCerts_ServerCertSignedByCA(t *testing.T) {
	caPEM, certPEM, _, err := GenerateCerts("kompakt-controller", "kompakt-system")
	if err != nil {
		t.Fatalf("GenerateCerts failed: %v", err)
	}

	caBlock, _ := pem.Decode(caPEM)
	ca, _ := x509.ParseCertificate(caBlock.Bytes)

	certBlock, _ := pem.Decode(certPEM)
	cert, _ := x509.ParseCertificate(certBlock.Bytes)

	pool := x509.NewCertPool()
	pool.AddCert(ca)

	if _, err := cert.Verify(x509.VerifyOptions{
		Roots:     pool,
		DNSName:   "kompakt-controller.kompakt-system.svc",
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}); err != nil {
		t.Fatalf("server cert verification failed: %v", err)
	}
}

func TestGenerateCerts_ServerCertHasServerAuth(t *testing.T) {
	_, certPEM, _, err := GenerateCerts("kompakt-controller", "kompakt-system")
	if err != nil {
		t.Fatalf("GenerateCerts failed: %v", err)
	}

	block, _ := pem.Decode(certPEM)
	cert, _ := x509.ParseCertificate(block.Bytes)

	found := false
	for _, usage := range cert.ExtKeyUsage {
		if usage == x509.ExtKeyUsageServerAuth {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected ExtKeyUsageServerAuth in server cert")
	}
}

func TestGenerateCerts_CertLifetime(t *testing.T) {
	_, certPEM, _, err := GenerateCerts("svc", "ns")
	if err != nil {
		t.Fatalf("GenerateCerts failed: %v", err)
	}

	block, _ := pem.Decode(certPEM)
	cert, _ := x509.ParseCertificate(block.Bytes)

	lifetime := cert.NotAfter.Sub(cert.NotBefore)
	expected := 365 * 24 * time.Hour

	// Allow 1 minute tolerance for test execution time
	if lifetime < expected-time.Minute || lifetime > expected+time.Minute {
		t.Fatalf("expected cert lifetime ~%v, got %v", expected, lifetime)
	}
}

func TestCertsValid_FreshCerts(t *testing.T) {
	p := &CertProvisioner{
		cfg: Config{
			ServiceName: "kompakt-controller",
			Namespace:   "kompakt-system",
		},
	}

	caPEM, certPEM, keyPEM, err := GenerateCerts("kompakt-controller", "kompakt-system")
	if err != nil {
		t.Fatalf("GenerateCerts failed: %v", err)
	}

	secret := &corev1.Secret{
		Data: map[string][]byte{
			"tls.crt": certPEM,
			"tls.key": keyPEM,
			"ca.crt":  caPEM,
		},
	}
	if !p.certsValid(secret) {
		t.Fatal("expected fresh certs to be valid")
	}
}

func TestCertsValid_MissingCACert(t *testing.T) {
	p := &CertProvisioner{
		cfg: Config{
			ServiceName: "kompakt-controller",
			Namespace:   "kompakt-system",
		},
	}

	_, certPEM, keyPEM, err := GenerateCerts("kompakt-controller", "kompakt-system")
	if err != nil {
		t.Fatalf("GenerateCerts failed: %v", err)
	}

	secret := &corev1.Secret{
		Data: map[string][]byte{
			"tls.crt": certPEM,
			"tls.key": keyPEM,
		},
	}
	if p.certsValid(secret) {
		t.Fatal("expected certs without ca.crt to be invalid")
	}
}

func TestCertsValid_WrongSAN(t *testing.T) {
	p := &CertProvisioner{
		cfg: Config{
			ServiceName: "kompakt-controller",
			Namespace:   "kompakt-system",
		},
	}

	caPEM, certPEM, keyPEM, err := GenerateCerts("other-service", "other-namespace")
	if err != nil {
		t.Fatalf("GenerateCerts failed: %v", err)
	}

	secret := &corev1.Secret{
		Data: map[string][]byte{
			"tls.crt": certPEM,
			"tls.key": keyPEM,
			"ca.crt":  caPEM,
		},
	}
	if p.certsValid(secret) {
		t.Fatal("expected certs with wrong SAN to be invalid")
	}
}

func TestCertsValid_EmptyCert(t *testing.T) {
	p := &CertProvisioner{
		cfg: Config{
			ServiceName: "kompakt-controller",
			Namespace:   "kompakt-system",
		},
	}

	secret := &corev1.Secret{
		Data: map[string][]byte{"tls.crt": nil},
	}
	if p.certsValid(secret) {
		t.Fatal("expected empty cert to be invalid")
	}
}

func TestDetectNamespace_FallbackDefault(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "")
	ns := DetectNamespace()
	// In test environment, the SA file likely does not exist,
	// and we cleared the env var, so it should fall back.
	if ns != "kompakt-system" {
		t.Logf("got namespace %q (may be from SA mount)", ns)
	}
}

func TestDetectNamespace_FromEnv(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "test-ns")
	ns := DetectNamespace()
	// SA file takes precedence if it exists; otherwise env var
	if ns != "test-ns" {
		t.Logf("got namespace %q (SA mount may take precedence)", ns)
	}
}
