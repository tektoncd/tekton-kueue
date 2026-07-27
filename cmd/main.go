/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"crypto/tls"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"time"

	"github.com/konflux-ci/tekton-kueue/pkg/common"
	"github.com/konflux-ci/tekton-kueue/pkg/mutate"
	"sigs.k8s.io/controller-runtime/pkg/cache"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/certwatcher"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	"github.com/konflux-ci/tekton-kueue/internal/controller"
	webhookv1 "github.com/konflux-ci/tekton-kueue/internal/webhook/v1"

	// +kubebuilder:scaffold:imports

	tekv1 "github.com/tektoncd/pipeline/pkg/apis/pipeline/v1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(kueue.AddToScheme(scheme))
	utilruntime.Must(tekv1.AddToScheme(scheme))
	// +kubebuilder:scaffold:scheme
}

type SharedFlags struct {
	ConfigDir       string
	MetricsAddr     string
	MetricsCertPath string
	MetricsCertName string
	MetricsCertKey  string
	SecureMetrics   bool
	ProbeAddr       string
	EnableHTTP2     bool
	ZapOptions      *zap.Options
	QPS             float64
	Burst           int
}

func (s *SharedFlags) AddFlags(fs *flag.FlagSet) {
	fs.StringVar(&s.ConfigDir, "config-dir", "", "The directory that contains the configuration file "+
		"for the tekton-kueue. ")
	fs.StringVar(&s.MetricsAddr, "metrics-bind-address", "0", "The address the metrics endpoint binds to. "+
		"Use :8443 for HTTPS or :8080 for HTTP, or leave as 0 to disable the metrics service.")
	fs.StringVar(&s.MetricsCertPath, "metrics-cert-path", "",
		"The directory that contains the metrics server certificate.")
	fs.StringVar(&s.MetricsCertName, "metrics-cert-name", "tls.crt", "The name of the metrics server certificate file.")
	fs.StringVar(&s.MetricsCertKey, "metrics-cert-key", "tls.key", "The name of the metrics server key file.")
	fs.BoolVar(&s.SecureMetrics, "metrics-secure", true,
		"If set, the metrics endpoint is served securely via HTTPS. Use --metrics-secure=false to use HTTP instead.")
	fs.StringVar(&s.ProbeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	fs.BoolVar(&s.EnableHTTP2, "enable-http2", false,
		"If set, HTTP/2 will be enabled for the metrics and webhook servers")
	fs.Float64Var(&s.QPS, "qps", 0, "The Kubernetes API Client's QPS value")
	fs.IntVar(&s.Burst, "burst", 0, "The Kubernetes API Client's Burst value")

	s.ZapOptions = &zap.Options{
		Development: true,
	}
	s.ZapOptions.BindFlags(fs)
	config.RegisterFlags(fs)
}

type ControllerFlags struct {
	SharedFlags
	EnableLeaderElection bool
	LeaseDuration        time.Duration
	RenewDeadline        time.Duration
	RetryPeriod          time.Duration
}

func (c *ControllerFlags) AddFlags(fs *flag.FlagSet) {
	c.SharedFlags.AddFlags(fs)
	fs.BoolVar(&c.EnableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	fs.DurationVar(
		&c.LeaseDuration,
		"leader-elect-lease-duration",
		15*time.Second,
		"The duration that non-leader candidates will wait after observing a "+
			"	leadership renewal until attempting to acquire leadership.",
	)
	fs.DurationVar(
		&c.RenewDeadline,
		"leader-elect-renew-deadline",
		10*time.Second,
		"The interval between attempts by the acting master to renew a leadership slot before it stops leading.",
	)
	fs.DurationVar(&c.RetryPeriod, "leader-elect-retry-period", 2*time.Second,
		"The duration the clients should wait between attempting acquisition and renewal of a leadership.")
}

type WebhookFlags struct {
	SharedFlags
	WebhookCertPath string
	WebhookCertName string
	WebhookCertKey  string
}

func (w *WebhookFlags) AddFlags(fs *flag.FlagSet) {
	w.SharedFlags.AddFlags(fs)
	fs.StringVar(&w.WebhookCertPath, "webhook-cert-path", "", "The directory that contains the webhook certificate.")
	fs.StringVar(&w.WebhookCertName, "webhook-cert-name", "tls.crt", "The name of the webhook certificate file.")
	fs.StringVar(&w.WebhookCertKey, "webhook-cert-key", "tls.key", "The name of the webhook key file.")
}

type MutateFlags struct {
	PipelineRunFile string
	ConfigDir       string
	ZapOptions      *zap.Options
}

func (m *MutateFlags) AddFlags(fs *flag.FlagSet) {
	fs.StringVar(&m.PipelineRunFile, "pipelinerun-file", "",
		"Path to the file containing the PipelineRun definition (required)")
	fs.StringVar(&m.ConfigDir, "config-dir", "",
		"The directory that contains the configuration file for the tekton-kueue (required)")
	m.ZapOptions = &zap.Options{
		Development: true,
	}
	m.ZapOptions.BindFlags(fs)
}

func main() {
	expectedSubcommands := "expected 'controller', 'webhook', or 'mutate' subcommand"
	if len(os.Args) < 2 {
		fmt.Println(expectedSubcommands)
		os.Exit(1)
	}

	switch os.Args[1] {
	case "controller":
		runController(os.Args[2:])
	case "webhook":
		runWebhook(os.Args[2:])
	case "mutate":
		runMutate(os.Args[2:])
	default:
		fmt.Printf("Got subcommand %s, %s", os.Args[1], expectedSubcommands)
		os.Exit(1)
	}
}

func runController(args []string) {
	fs := flag.NewFlagSet("controller", flag.ExitOnError)
	var controllerFlags ControllerFlags
	controllerFlags.AddFlags(fs)

	parseFlagsOrDie(fs, args)
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(controllerFlags.ZapOptions)))
	tlsOpts := getTLSOpts(&controllerFlags.SharedFlags)
	metricsServerOptions, metricsCertWatcher := getMetricsServerOptions(&controllerFlags.SharedFlags, tlsOpts)

	// Log leader election configuration
	const leaderElectionId = "f2ddafa2.konflux-ci.dev"
	if controllerFlags.EnableLeaderElection {
		setupLog.Info("Leader election enabled with lease configuration",
			"lease-duration", controllerFlags.LeaseDuration,
			"renew-deadline", controllerFlags.RenewDeadline,
			"retry-period", controllerFlags.RetryPeriod,
			"leader-election-id", leaderElectionId)
	} else {
		setupLog.Info("Leader election disabled")
	}

	cfg := ctrl.GetConfigOrDie()
	// if Burst is set to 0, controller-runtime Client's default is used
	cfg.Burst = controllerFlags.Burst
	// ensure the provided value is a safe float32. If it's not, we set 0 and the
	// controller-runtime's Client will use its default value.
	qps, ok := safeFloat64ToFloat32OrZero(controllerFlags.QPS)
	if !ok {
		setupLog.
			WithValues("provided-qps", controllerFlags.QPS, "used-qps", qps).
			Info("provided QPS value is not a valid float32, using default")
	}
	cfg.QPS = qps

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsServerOptions,
		HealthProbeBindAddress: controllerFlags.ProbeAddr,
		LeaderElection:         controllerFlags.EnableLeaderElection,
		LeaderElectionID:       leaderElectionId,
		LeaseDuration:          &controllerFlags.LeaseDuration,
		RenewDeadline:          &controllerFlags.RenewDeadline,
		RetryPeriod:            &controllerFlags.RetryPeriod,

		// LeaderElectionReleaseOnCancel defines if the leader should step down voluntarily
		// when the Manager ends. This requires the binary to immediately end when the
		// Manager is stopped, otherwise, this setting is unsafe. Setting this significantly
		// speeds up voluntary leader transitions as the new leader don't have to wait
		// LeaseDuration time first.
		//
		// In the default scaffold provided, the program ends immediately after
		// the manager stops, so would be fine to enable this option. However,
		// if you are doing or is intended to do any operation such as perform cleanups
		// after the manager stops then its usage might be unsafe.
		// LeaderElectionReleaseOnCancel: true,
	})
	if err != nil {
		setupLog.Error(err, "unable to create manager")
		os.Exit(1)
	}

	ctx := ctrl.SetupSignalHandler()
	err = controller.SetupWithManager(ctx, mgr)
	if err != nil {
		setupLog.Error(err, "Failed to setup the controller")
		os.Exit(1)
	}

	err = controller.SetupIndexer(ctx, mgr.GetFieldIndexer())
	if err != nil {
		setupLog.Error(err, "Failed to setup the indexer")
		os.Exit(1)
	}

	addMetricsCertWatcher(mgr, metricsCertWatcher)
	addReadyAndHealthChecksToMgrOrDie(mgr)
	addMultiKueueReconciler(mgr)

	setupLog.Info("starting manager")
	if err := mgr.Start(ctx); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}

func runWebhook(args []string) {
	setupLog.Info("starting webhook")
	fs := flag.NewFlagSet("webhook", flag.ExitOnError)
	var webhookFlags WebhookFlags
	webhookFlags.AddFlags(fs)
	parseFlagsOrDie(fs, args)
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(webhookFlags.ZapOptions)))
	tlsOpts := getTLSOpts(&webhookFlags.SharedFlags)
	metricsServerOptions, metricsCertWatcher := getMetricsServerOptions(&webhookFlags.SharedFlags, tlsOpts)

	webhookOptions, webhookCertWatcher := getWebhookServerOptions(webhookFlags, tlsOpts)
	webhookServer := webhook.NewServer(webhookOptions)
	namespace, err := common.GetCurrentNamespace()
	if err != nil {
		setupLog.Error(err, "Exiting")
		os.Exit(1)
	}
	cfg := ctrl.GetConfigOrDie()
	// if Burst is set to 0, controller-runtime Client's default is used
	cfg.Burst = webhookFlags.Burst
	// ensure the provided value is a safe float32. If it's not, we set 0 and the
	// controller-runtime's Client will use its default value.
	qps, ok := safeFloat64ToFloat32OrZero(webhookFlags.QPS)
	if !ok {
		setupLog.
			WithValues("provided-qps", webhookFlags.QPS, "used-qps", qps).
			Info("provided QPS value is not a valid float32, using default")
	}
	cfg.QPS = qps

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsServerOptions,
		HealthProbeBindAddress: webhookFlags.ProbeAddr,
		WebhookServer:          webhookServer,
		LeaderElection:         false,
		Cache: cache.Options{
			DefaultNamespaces: map[string]cache.Config{
				namespace: {}, // namespace where SA has access
			},
		},
	})
	if err != nil {
		setupLog.Error(err, "unable to create manager")
		os.Exit(1)
	}
	cfgStore := &webhookv1.ConfigStore{}
	customDefaulter, err := webhookv1.NewCustomDefaulter(cfgStore)
	if err != nil {
		setupLog.Error(err, "unable to create custom defaulter")
		os.Exit(1)
	}
	err = webhookv1.SetupPipelineRunWebhookWithManager(
		mgr,
		customDefaulter,
	)
	if err != nil {
		setupLog.Error(err, "Failed to setup the webhook")
		os.Exit(1)
	}
	addConfigWatcher(mgr, cfgStore)
	addRunnableOrDie(
		mgr,
		webhookCertWatcher,
		"Adding webhook certificate watcher to manager",
		"unable to add webhook certificate watcher to manager",
	)
	addMetricsCertWatcher(mgr, metricsCertWatcher)
	addReadyAndHealthChecksToMgrOrDie(mgr)

	setupLog.Info("starting manager")
	ctx := ctrl.SetupSignalHandler()
	if err := mgr.Start(ctx); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}

func safeFloat64ToFloat32OrZero(v float64) (float32, bool) {
	if math.IsNaN(v) || math.IsInf(v, 0) || v < -math.MaxFloat32 || v > math.MaxFloat32 {
		return 0, false
	}
	return float32(v), true
}

func runMutate(args []string) {
	fs := flag.NewFlagSet("mutate", flag.ExitOnError)
	var mutateFlags MutateFlags
	mutateFlags.AddFlags(fs)

	parseFlagsOrDie(fs, args)
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(mutateFlags.ZapOptions)))

	// Validate required flags
	if mutateFlags.PipelineRunFile == "" {
		fmt.Fprintf(os.Stderr, "Error: --pipelinerun-file is required\n")
		fs.Usage()
		os.Exit(1)
	}
	if mutateFlags.ConfigDir == "" {
		fmt.Fprintf(os.Stderr, "Error: --config-dir is required\n")
		fs.Usage()
		os.Exit(1)
	}

	// Use the mutate package to perform the mutation
	mutatedData, err := mutate.MutatePipelineRun(mutateFlags.PipelineRunFile, mutateFlags.ConfigDir)
	if err != nil {
		setupLog.Error(err, "Failed to mutate PipelineRun")
		os.Exit(1)
	}

	fmt.Print(string(mutatedData))
}

func getTLSOpts(s *SharedFlags) []func(*tls.Config) {
	var tlsOpts []func(*tls.Config)
	// if the enable-http2 flag is false (the default), http/2 should be disabled
	// due to its vulnerabilities. More specifically, disabling http/2 will
	// prevent from being vulnerable to the HTTP/2 Stream Cancellation and
	// Rapid Reset CVEs. For more information see:
	// - https://github.com/advisories/GHSA-qppj-fm5r-hxr3
	// - https://github.com/advisories/GHSA-4374-p667-p6c8
	disableHTTP2 := func(c *tls.Config) {
		setupLog.Info("disabling http/2")
		c.NextProtos = []string{"http/1.1"}
	}

	if !s.EnableHTTP2 {
		tlsOpts = append(tlsOpts, disableHTTP2)
	}

	return tlsOpts
}

func getMetricsServerOptions(
	s *SharedFlags,
	tlsOpts []func(*tls.Config),
) (metricsserver.Options, *certwatcher.CertWatcher) {
	// Metrics endpoint is enabled in 'config/default/kustomization.yaml'. The Metrics options configure the server.
	// More info:
	// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.20.0/pkg/metrics/server
	// - https://book.kubebuilder.io/reference/metrics.html
	metricsServerOptions := metricsserver.Options{
		BindAddress:   s.MetricsAddr,
		SecureServing: s.SecureMetrics,
		TLSOpts:       tlsOpts,
	}

	if s.SecureMetrics {
		// FilterProvider is used to protect the metrics endpoint with authn/authz.
		// These configurations ensure that only authorized users and service accounts
		// can access the metrics endpoint. The RBAC are configured in 'config/rbac/kustomization.yaml'. More info:
		// https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.20.0/pkg/metrics/filters#WithAuthenticationAndAuthorization
		metricsServerOptions.FilterProvider = filters.WithAuthenticationAndAuthorization
	}

	// If the certificate is not specified, controller-runtime will automatically
	// generate self-signed certificates for the metrics server. While convenient for development and testing,
	// this setup is not recommended for production.
	//
	// TODO(user): If you enable certManager, uncomment the following lines:
	// - [METRICS-WITH-CERTS] at config/default/kustomization.yaml to generate and use certificates
	// managed by cert-manager for the metrics server.
	// - [PROMETHEUS-WITH-CERTS] at config/prometheus/kustomization.yaml for TLS certification.

	var metricsCertWatcher *certwatcher.CertWatcher = nil
	if len(s.MetricsCertPath) > 0 {
		setupLog.Info("Initializing metrics certificate watcher using provided certificates",
			"metrics-cert-path", s.MetricsCertPath, "metrics-cert-name", s.MetricsCertName, "metrics-cert-key", s.MetricsCertKey)

		var err error
		metricsCertWatcher, err = certwatcher.New(
			filepath.Join(s.MetricsCertPath, s.MetricsCertName),
			filepath.Join(s.MetricsCertPath, s.MetricsCertKey),
		)
		if err != nil {
			setupLog.Error(err, "to initialize metrics certificate watcher", "error", err)
			os.Exit(1)
		}

		metricsServerOptions.TLSOpts = append(metricsServerOptions.TLSOpts, func(config *tls.Config) {
			config.GetCertificate = metricsCertWatcher.GetCertificate
		})
	}

	return metricsServerOptions, metricsCertWatcher
}

func getWebhookServerOptions(
	webhookFlags WebhookFlags,
	tlsOpts []func(*tls.Config),
) (webhook.Options, *certwatcher.CertWatcher) {
	webhookOptions := webhook.Options{}
	var webhookCertWatcher *certwatcher.CertWatcher = nil
	webhookTLSOpts := tlsOpts

	if len(webhookFlags.WebhookCertPath) == 0 {
		return webhookOptions, webhookCertWatcher
	}

	setupLog.Info(
		"Initializing webhook certificate watcher using provided certificates",
		"webhook-cert-path",
		webhookFlags.WebhookCertPath,
		"webhook-cert-name",
		webhookFlags.WebhookCertName,
		"webhook-cert-key",
		webhookFlags.WebhookCertKey,
	)

	webhookCertWatcher, err := certwatcher.New(
		filepath.Join(webhookFlags.WebhookCertPath, webhookFlags.WebhookCertName),
		filepath.Join(webhookFlags.WebhookCertPath, webhookFlags.WebhookCertKey),
	)
	if err != nil {
		setupLog.Error(err, "Failed to initialize webhook certificate watcher")
		os.Exit(1)
	}

	webhookTLSOpts = append(webhookTLSOpts, func(config *tls.Config) {
		config.GetCertificate = webhookCertWatcher.GetCertificate
	})
	webhookOptions.TLSOpts = webhookTLSOpts

	return webhookOptions, webhookCertWatcher
}

func addReadyAndHealthChecksToMgrOrDie(mgr manager.Manager) {
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}
}

func addRunnableOrDie(mgr ctrl.Manager, runnable manager.Runnable, infoMsg, errMsg string) {
	if reflect.ValueOf(runnable).IsNil() {
		return
	}
	setupLog.Info(infoMsg)
	if err := mgr.Add(runnable); err != nil {
		setupLog.Error(err, errMsg)
		os.Exit(1)
	}
}

func addMetricsCertWatcher(mgr ctrl.Manager, runnable manager.Runnable) {
	addRunnableOrDie(
		mgr,
		runnable,
		"Adding metrics certificate watcher to manager",
		"unable to add webhook certificate watcher to manager",
	)
}

func addConfigWatcher(mgr ctrl.Manager, configStore *webhookv1.ConfigStore) {
	setupLog.Info("Adding config watcher to manager")
	reconciler := controller.ConfigMapReconciler{
		Client: mgr.GetClient(),
		Store:  configStore,
	}
	err := reconciler.SetupWithManager(mgr)
	if err != nil {
		setupLog.Error(err, "Failed to add watcher to config ")
		os.Exit(1)
	}
}

func addMultiKueueReconciler(mgr ctrl.Manager) {
	reconciler := &controller.MultiKueueReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}

	if err := reconciler.SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "SpokeCredential")
		os.Exit(1)
	}
}

func parseFlagsOrDie(fs *flag.FlagSet, args []string) {
	if err := fs.Parse(args); err != nil {
		setupLog.Error(err, "Failed to parse CLI arguments")
		os.Exit(1)
	}
}
