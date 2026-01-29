package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/uuid"
	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	"k8s.io/klog/v2"
	"k8s.io/utils/clock"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configv1 "github.com/openshift/api/config/v1"
	machinev1beta1 "github.com/openshift/api/machine/v1beta1"
	configclient "github.com/openshift/client-go/config/clientset/versioned"
	machineclient "github.com/openshift/client-go/machine/clientset/versioned"
	"github.com/openshift/library-go/pkg/operator/events"
	migrationv1alpha1 "github.com/openshift/vsphere-migration-controller/pkg/apis/migration/v1alpha1"
	"github.com/openshift/vsphere-migration-controller/pkg/controller"
	corev1 "k8s.io/api/core/v1"
)

const (
	leaseLockName      = "vsphere-migration-controller"
	leaseLockNamespace = "openshift-vsphere-migration"
)

var (
	kubeconfig       string
	masterURL        string
	enableLeaderElect bool
)

func init() {
	flag.StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig file")
	flag.StringVar(&masterURL, "master", "", "Kubernetes API server URL")
	flag.BoolVar(&enableLeaderElect, "leader-elect", true, "Enable leader election for controller manager")
}

func main() {
	klog.InitFlags(nil)
	flag.Parse()

	// Set up signal handling
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	signalCh := make(chan os.Signal, 1)
	signal.Notify(signalCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-signalCh
		klog.Info("Received shutdown signal")
		cancel()
	}()

	logger := klog.NewKlogr().WithName("vsphere-migration-controller")
	ctx = klog.NewContext(ctx, logger)

	logger.Info("Starting vSphere Migration Controller")

	// Build Kubernetes config
	config, err := buildConfig(kubeconfig, masterURL)
	if err != nil {
		logger.Error(err, "Failed to build Kubernetes config")
		os.Exit(1)
	}

	// Create clients
	kubeClient, err := kubernetes.NewForConfig(config)
	if err != nil {
		logger.Error(err, "Failed to create Kubernetes client")
		os.Exit(1)
	}

	configClient, err := configclient.NewForConfig(config)
	if err != nil {
		logger.Error(err, "Failed to create config client")
		os.Exit(1)
	}

	// Create dynamic client for VSphereMigration resources
	dynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		logger.Error(err, "Failed to create dynamic client")
		os.Exit(1)
	}

	// Create apiextensions client for CRD manipulation
	apiextensionsClient, err := apiextensionsclient.NewForConfig(config)
	if err != nil {
		logger.Error(err, "Failed to create apiextensions client")
		os.Exit(1)
	}

	// Create scheme
	scheme := runtime.NewScheme()
	if err := migrationv1alpha1.AddToScheme(scheme); err != nil {
		logger.Error(err, "Failed to add migration API to scheme")
		os.Exit(1)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		logger.Error(err, "Failed to add core API to scheme")
		os.Exit(1)
	}
	if err := configv1.AddToScheme(scheme); err != nil {
		logger.Error(err, "Failed to add config API to scheme")
		os.Exit(1)
	}
	if err := machinev1beta1.AddToScheme(scheme); err != nil {
		logger.Error(err, "Failed to add machine API to scheme")
		os.Exit(1)
	}

	// Create machine client
	machineClient, err := machineclient.NewForConfig(config)
	if err != nil {
		logger.Error(err, "Failed to create machine client")
		os.Exit(1)
	}

	// Create controller-runtime client
	runtimeClient, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		logger.Error(err, "Failed to create controller-runtime client")
		os.Exit(1)
	}

	// Create event recorder
	eventRecorder := events.NewLoggingEventRecorder("vsphere-migration-controller", clock.RealClock{})

	// Create controller
	migrationController, factoryController := controller.NewMigrationController(
		kubeClient,
		configClient,
		machineClient,
		dynamicClient,
		apiextensionsClient,
		runtimeClient,
		scheme,
		eventRecorder,
	)

	// Set up informer for VSphereMigration resources
	gvr := schema.GroupVersionResource{
		Group:    "migration.openshift.io",
		Version:  "v1alpha1",
		Resource: "vspheremigrations",
	}

	// Create dynamic informer factory
	informerFactory := dynamicinformer.NewDynamicSharedInformerFactory(dynamicClient, 10*time.Minute)
	migrationInformer := informerFactory.ForResource(gvr)

	// Add event handler
	migrationInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			logger.Info("VSphereMigration added", "obj", obj)
			migrationController.EnqueueMigration(obj)
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			logger.Info("VSphereMigration updated")
			migrationController.EnqueueMigration(newObj)
		},
		DeleteFunc: func(obj interface{}) {
			logger.Info("VSphereMigration deleted")
		},
	})

	// Define the run function that starts the controller
	run := func(ctx context.Context) {
		logger.Info("Starting informers")
		informerFactory.Start(ctx.Done())

		// Wait for cache sync
		logger.Info("Waiting for informer cache sync")
		if !cache.WaitForCacheSync(ctx.Done(), migrationInformer.Informer().HasSynced) {
			logger.Error(nil, "Failed to sync informer cache")
			os.Exit(1)
		}
		logger.Info("Informer cache synced")

		logger.Info("Starting controller")
		go factoryController.Run(ctx, 1)

		logger.Info("Controller started, waiting for shutdown signal")
		<-ctx.Done()
		logger.Info("Shutting down controller")
	}

	// Run with or without leader election
	if !enableLeaderElect {
		logger.Info("Leader election disabled, running directly")
		run(ctx)
		return
	}

	// Generate unique identity for this instance
	id, err := os.Hostname()
	if err != nil {
		id = uuid.New().String()
	}
	id = id + "_" + uuid.New().String()

	logger.Info("Starting with leader election", "identity", id)

	// Create resource lock for leader election
	lock := &resourcelock.LeaseLock{
		LeaseMeta: metav1.ObjectMeta{
			Name:      leaseLockName,
			Namespace: leaseLockNamespace,
		},
		Client: kubeClient.CoordinationV1(),
		LockConfig: resourcelock.ResourceLockConfig{
			Identity: id,
		},
	}

	// Start leader election
	leaderelection.RunOrDie(ctx, leaderelection.LeaderElectionConfig{
		Lock:            lock,
		ReleaseOnCancel: true,
		LeaseDuration:   15 * time.Second,
		RenewDeadline:   10 * time.Second,
		RetryPeriod:     2 * time.Second,
		Callbacks: leaderelection.LeaderCallbacks{
			OnStartedLeading: func(ctx context.Context) {
				logger.Info("Acquired leadership")
				run(ctx)
			},
			OnStoppedLeading: func() {
				logger.Info("Lost leadership, shutting down")
				os.Exit(0)
			},
			OnNewLeader: func(identity string) {
				if identity == id {
					return
				}
				logger.Info("New leader elected", "leader", identity)
			},
		},
	})
}

// buildConfig builds a Kubernetes config from flags
func buildConfig(kubeconfig, masterURL string) (*rest.Config, error) {
	if kubeconfig != "" {
		return clientcmd.BuildConfigFromFlags(masterURL, kubeconfig)
	}
	return rest.InClusterConfig()
}
