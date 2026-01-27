package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"syscall"
	"time"

	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"
	"k8s.io/utils/clock"

	configclient "github.com/openshift/client-go/config/clientset/versioned"
	"github.com/openshift/library-go/pkg/operator/events"
	migrationv1alpha1 "github.com/openshift/vsphere-migration-controller/pkg/apis/migration/v1alpha1"
	"github.com/openshift/vsphere-migration-controller/pkg/controller"
)

var (
	kubeconfig  string
	masterURL   string
)

func init() {
	flag.StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig file")
	flag.StringVar(&masterURL, "master", "", "Kubernetes API server URL")
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

	// Create event recorder
	eventRecorder := events.NewLoggingEventRecorder("vsphere-migration-controller", clock.RealClock{})

	// Create controller
	migrationController, factoryController := controller.NewMigrationController(
		kubeClient,
		configClient,
		dynamicClient,
		apiextensionsClient,
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

// buildConfig builds a Kubernetes config from flags
func buildConfig(kubeconfig, masterURL string) (*rest.Config, error) {
	if kubeconfig != "" {
		return clientcmd.BuildConfigFromFlags(masterURL, kubeconfig)
	}
	return rest.InClusterConfig()
}
