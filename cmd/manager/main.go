package main

import (
	"context"
	"flag"
	"os"

	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	webhookadmission "sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	packingv1alpha1 "github.com/reyshazni/kompakt/api/v1alpha1"
	"github.com/reyshazni/kompakt/internal/certgen"
	kompaktcontroller "github.com/reyshazni/kompakt/internal/controller"
	"github.com/reyshazni/kompakt/internal/inflight"
	"github.com/reyshazni/kompakt/internal/ledger"
	"github.com/reyshazni/kompakt/internal/matcher"
	kompaktwebhook "github.com/reyshazni/kompakt/internal/webhook"
)

var scheme = runtime.NewScheme()

func init() {
	_ = corev1.AddToScheme(scheme)
	_ = coordinationv1.AddToScheme(scheme)
	_ = packingv1alpha1.AddToScheme(scheme)
}

const certDir = "/tmp/k8s-webhook-server/serving-certs"

func main() {
	var leaderElect bool
	flag.BoolVar(&leaderElect, "leader-elect", false, "enable leader election for HA")
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseDevMode(false)))
	log := ctrl.Log.WithName("manager")

	restCfg := ctrl.GetConfigOrDie()

	// Set up cert provisioner (typed client for Secrets + webhook configs)
	typedClient, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		log.Error(err, "unable to create typed client")
		os.Exit(1)
	}

	certProvisioner := certgen.New(certgen.Config{
		Namespace:         certgen.DetectNamespace(),
		ServiceName:       "kompakt-controller",
		SecretName:        "kompakt-webhook-certs",
		WebhookConfigName: "kompakt-webhook",
		CertDir:           certDir,
	}, typedClient)

	// Provision certs synchronously before manager starts so the webhook
	// server finds cert files when it begins TLS.
	if err := certProvisioner.EnsureCerts(context.Background()); err != nil {
		log.Error(err, "unable to provision webhook certs")
		os.Exit(1)
	}

	mgr, err := ctrl.NewManager(restCfg, ctrl.Options{
		Scheme: scheme,
		WebhookServer: webhook.NewServer(webhook.Options{
			Port:    9443,
			CertDir: certDir,
		}),
		Metrics: metricsserver.Options{
			BindAddress: ":8080",
		},
		HealthProbeBindAddress: ":8081",
		LeaderElection:         leaderElect,
		LeaderElectionID:       "kompakt.leader.election",
	})
	if err != nil {
		log.Error(err, "unable to create manager")
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		log.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", certProvisioner.ReadyzCheck); err != nil {
		log.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	// Register cert provisioner (runs on all replicas, no leader election)
	if err := mgr.Add(certProvisioner); err != nil {
		log.Error(err, "unable to register cert provisioner")
		os.Exit(1)
	}

	// Set up webhook
	resolver := matcher.NewProfileResolver(mgr.GetAPIReader())
	injector := kompaktwebhook.NewPodGateInjector(resolver)
	mgr.GetWebhookServer().Register("/mutate-v1-pod", &webhookadmission.Webhook{Handler: injector})

	// Set up controller
	nodeLedger := ledger.New()
	detectors := []inflight.Detector{&inflight.ClusterAutoscalerDetector{}}
	reconciler := &kompaktcontroller.PackingProfileReconciler{
		Client:    mgr.GetClient(),
		APIReader: mgr.GetAPIReader(),
		Ledger:    nodeLedger,
		Detectors: detectors,
		Recorder:  mgr.GetEventRecorderFor("kompakt-controller"), //nolint:staticcheck // new API not compatible with record.EventRecorder
	}
	if err := reconciler.SetupWithManager(mgr); err != nil {
		log.Error(err, "unable to set up controller")
		os.Exit(1)
	}

	log.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		log.Error(err, "manager exited with error")
		os.Exit(1)
	}
}
