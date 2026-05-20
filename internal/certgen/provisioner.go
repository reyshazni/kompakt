package certgen

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
)

const (
	caLifetime    = 10 * 365 * 24 * time.Hour // 10 years
	certLifetime  = 365 * 24 * time.Hour      // 1 year
	rotateAt      = 0.80                      // rotate at 80% of lifetime
	checkInterval = 12 * time.Hour
)

// Config holds the parameters for the cert provisioner.
type Config struct {
	Namespace         string
	ServiceName       string
	SecretName        string
	WebhookConfigName string
	CertDir           string
}

// CertProvisioner generates self-signed TLS certificates for the webhook server,
// stores them in a Kubernetes Secret, patches the MutatingWebhookConfiguration
// caBundle, and rotates certs before expiry.
type CertProvisioner struct {
	cfg    Config
	client kubernetes.Interface
	log    logr.Logger
}

// New creates a CertProvisioner.
func New(cfg Config, client kubernetes.Interface) *CertProvisioner {
	return &CertProvisioner{
		cfg:    cfg,
		client: client,
		log:    ctrl.Log.WithName("certgen"),
	}
}

// NeedLeaderElection returns false so the provisioner runs on all replicas.
// Every pod needs cert files on disk for the webhook server.
func (p *CertProvisioner) NeedLeaderElection() bool {
	return false
}

// EnsureCerts checks existing certs and provisions new ones if needed.
// Call this synchronously before starting the manager so cert files exist
// when the webhook server starts TLS.
func (p *CertProvisioner) EnsureCerts(ctx context.Context) error {
	return p.ensureCerts(ctx)
}

// Start implements manager.Runnable. It runs a background ticker to check
// for cert rotation. Initial provisioning must be done via EnsureCerts()
// before the manager starts.
func (p *CertProvisioner) Start(ctx context.Context) error {
	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := p.ensureCerts(ctx); err != nil {
				p.log.Error(err, "cert rotation check failed, will retry")
			}
		}
	}
}

// ReadyzCheck verifies that TLS cert files exist on disk and are parseable.
// Used as the readiness probe -- the pod is not ready to serve webhook traffic
// until certs are provisioned.
func (p *CertProvisioner) ReadyzCheck(_ *http.Request) error {
	certPath := filepath.Join(p.cfg.CertDir, "tls.crt")
	keyPath := filepath.Join(p.cfg.CertDir, "tls.key")

	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return fmt.Errorf("cert file not found: %w", err)
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return fmt.Errorf("key file not found: %w", err)
	}

	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return fmt.Errorf("invalid cert/key pair: %w", err)
	}

	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return fmt.Errorf("cannot parse cert: %w", err)
	}

	if time.Now().After(leaf.NotAfter) {
		return fmt.Errorf("cert expired at %s", leaf.NotAfter.Format(time.RFC3339))
	}

	return nil
}

func (p *CertProvisioner) ensureCerts(ctx context.Context) error {
	secret, err := p.client.CoreV1().Secrets(p.cfg.Namespace).Get(ctx, p.cfg.SecretName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		p.log.Info("cert secret not found, provisioning new certs")
		return p.provision(ctx)
	}
	if err != nil {
		return fmt.Errorf("get secret: %w", err)
	}

	if !p.certsValid(secret) {
		p.log.Info("certs invalid or approaching expiry, rotating")
		return p.provision(ctx)
	}

	// Certs are valid -- write files to disk (pod restart case)
	if err := p.writeFiles(secret.Data["tls.crt"], secret.Data["tls.key"]); err != nil {
		return fmt.Errorf("write cert files: %w", err)
	}

	return nil
}

// certsValid checks that the certs in the Secret are parseable, have correct SANs,
// and are not approaching expiry.
func (p *CertProvisioner) certsValid(secret *corev1.Secret) bool {
	certPEM := secret.Data["tls.crt"]
	if len(certPEM) == 0 || len(secret.Data["tls.key"]) == 0 || len(secret.Data["ca.crt"]) == 0 {
		return false
	}

	block, _ := pem.Decode(certPEM)
	if block == nil {
		return false
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return false
	}

	// Check SANs
	expectedDNS := p.cfg.ServiceName + "." + p.cfg.Namespace + ".svc"
	sanMatch := false
	for _, dns := range cert.DNSNames {
		if dns == expectedDNS {
			sanMatch = true
			break
		}
	}
	if !sanMatch {
		return false
	}

	// Check expiry (rotate at 80% of lifetime)
	lifetime := cert.NotAfter.Sub(cert.NotBefore)
	renewAt := cert.NotBefore.Add(time.Duration(float64(lifetime) * rotateAt))
	return time.Now().Before(renewAt)
}

// provision generates new certs, stores them in a Secret, writes files to disk,
// and patches the webhook configuration.
func (p *CertProvisioner) provision(ctx context.Context) error {
	caPEM, certPEM, keyPEM, err := GenerateCerts(p.cfg.ServiceName, p.cfg.Namespace)
	if err != nil {
		return fmt.Errorf("generate certs: %w", err)
	}

	if err := p.upsertSecret(ctx, caPEM, certPEM, keyPEM); err != nil {
		return fmt.Errorf("upsert secret: %w", err)
	}

	if err := p.writeFiles(certPEM, keyPEM); err != nil {
		return fmt.Errorf("write cert files: %w", err)
	}

	if err := p.patchWebhookCABundle(ctx, caPEM); err != nil {
		return fmt.Errorf("patch webhook caBundle: %w", err)
	}

	p.log.Info("certs provisioned successfully")
	return nil
}

// upsertSecret creates or updates the cert Secret.
func (p *CertProvisioner) upsertSecret(ctx context.Context, caPEM, certPEM, keyPEM []byte) error {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      p.cfg.SecretName,
			Namespace: p.cfg.Namespace,
		},
		Type: corev1.SecretTypeTLS,
		Data: map[string][]byte{
			"tls.crt": certPEM,
			"tls.key": keyPEM,
			"ca.crt":  caPEM,
		},
	}

	existing, err := p.client.CoreV1().Secrets(p.cfg.Namespace).Get(ctx, p.cfg.SecretName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = p.client.CoreV1().Secrets(p.cfg.Namespace).Create(ctx, secret, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}

	existing.Data = secret.Data
	_, err = p.client.CoreV1().Secrets(p.cfg.Namespace).Update(ctx, existing, metav1.UpdateOptions{})
	return err
}

// writeFiles writes the cert and key to the CertDir so the webhook server can read them.
func (p *CertProvisioner) writeFiles(certPEM, keyPEM []byte) error {
	if err := os.MkdirAll(p.cfg.CertDir, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(p.cfg.CertDir, "tls.crt"), certPEM, 0o600); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(p.cfg.CertDir, "tls.key"), keyPEM, 0o600)
}

// patchWebhookCABundle patches the MutatingWebhookConfiguration with the CA cert.
func (p *CertProvisioner) patchWebhookCABundle(ctx context.Context, caPEM []byte) error {
	mwc, err := p.client.AdmissionregistrationV1().MutatingWebhookConfigurations().Get(
		ctx, p.cfg.WebhookConfigName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get webhook config: %w", err)
	}

	for i := range mwc.Webhooks {
		mwc.Webhooks[i].ClientConfig.CABundle = caPEM
	}

	_, err = p.client.AdmissionregistrationV1().MutatingWebhookConfigurations().Update(
		ctx, mwc, metav1.UpdateOptions{})
	return err
}

// GenerateCerts creates a self-signed CA and a server certificate using ECDSA P-256.
// Returns PEM-encoded CA cert, server cert, and server key.
func GenerateCerts(serviceName, namespace string) (caPEM, certPEM, keyPEM []byte, err error) {
	// CA key + cert
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("generate CA key: %w", err)
	}

	caSerial, err := randomSerial()
	if err != nil {
		return nil, nil, nil, err
	}

	caTemplate := &x509.Certificate{
		SerialNumber:          caSerial,
		Subject:               pkix.Name{CommonName: serviceName + "-ca", Organization: []string{"kompakt"}},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(caLifetime),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		IsCA:                  true,
		BasicConstraintsValid: true,
	}

	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("create CA cert: %w", err)
	}

	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("parse CA cert: %w", err)
	}

	caPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})

	// Server key + cert
	serverKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("generate server key: %w", err)
	}

	serverSerial, err := randomSerial()
	if err != nil {
		return nil, nil, nil, err
	}

	dnsNames := []string{
		serviceName,
		serviceName + "." + namespace + ".svc",
		serviceName + "." + namespace + ".svc.cluster.local",
	}

	serverTemplate := &x509.Certificate{
		SerialNumber: serverSerial,
		Subject:      pkix.Name{CommonName: dnsNames[1]},
		DNSNames:     dnsNames,
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(certLifetime),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}

	serverDER, err := x509.CreateCertificate(rand.Reader, serverTemplate, caCert, &serverKey.PublicKey, caKey)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("create server cert: %w", err)
	}

	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: serverDER})

	keyDER, err := x509.MarshalECPrivateKey(serverKey)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("marshal server key: %w", err)
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	return caPEM, certPEM, keyPEM, nil
}

func randomSerial() (*big.Int, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("generate serial: %w", err)
	}
	return serial, nil
}

// DetectNamespace reads the namespace from the service account mount,
// falls back to POD_NAMESPACE env var, then to "kompakt-system".
func DetectNamespace() string {
	if data, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace"); err == nil {
		if ns := string(data); ns != "" {
			return ns
		}
	}
	if ns := os.Getenv("POD_NAMESPACE"); ns != "" {
		return ns
	}
	return "kompakt-system"
}
