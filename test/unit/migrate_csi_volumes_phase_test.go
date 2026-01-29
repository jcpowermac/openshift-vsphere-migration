package unit

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apiextensionsfake "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/fake"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"

	configv1 "github.com/openshift/api/config/v1"
	configfake "github.com/openshift/client-go/config/clientset/versioned/fake"
	machinefake "github.com/openshift/client-go/machine/clientset/versioned/fake"
	migrationv1alpha1 "github.com/openshift/vmware-cloud-foundation-migration/pkg/apis/migration/v1alpha1"
	"github.com/openshift/vmware-cloud-foundation-migration/pkg/backup"
	"github.com/openshift/vmware-cloud-foundation-migration/pkg/controller/phases"
	"github.com/openshift/vmware-cloud-foundation-migration/pkg/openshift"
)

func TestMigrateCSIVolumesPhase_Name(t *testing.T) {
	kubeClient := kubefake.NewSimpleClientset()
	configClient := configfake.NewSimpleClientset()
	scheme := runtime.NewScheme()

	backupMgr := backup.NewBackupManager(scheme)
	apiextensionsClient := apiextensionsfake.NewSimpleClientset()
	machineClient := machinefake.NewSimpleClientset()
	dynamicClient := dynamicfake.NewSimpleDynamicClient(scheme)
	executor := phases.NewPhaseExecutor(kubeClient, configClient, apiextensionsClient, machineClient, dynamicClient, backupMgr, nil)

	phase := phases.NewMigrateCSIVolumesPhase(executor)

	if phase.Name() != migrationv1alpha1.PhaseMigrateCSIVolumes {
		t.Errorf("expected phase name %s, got %s", migrationv1alpha1.PhaseMigrateCSIVolumes, phase.Name())
	}
}

func TestMigrateCSIVolumesPhase_Validate(t *testing.T) {
	tests := []struct {
		name        string
		migration   *migrationv1alpha1.VmwareCloudFoundationMigration
		expectError bool
	}{
		{
			name: "valid migration with failure domains",
			migration: &migrationv1alpha1.VmwareCloudFoundationMigration{
				Spec: migrationv1alpha1.VmwareCloudFoundationMigrationSpec{
					FailureDomains: []configv1.VSpherePlatformFailureDomainSpec{
						{
							Name:   "fd1",
							Server: "target-vcenter.example.com",
						},
					},
				},
			},
			expectError: false,
		},
		{
			name: "missing failure domains",
			migration: &migrationv1alpha1.VmwareCloudFoundationMigration{
				Spec: migrationv1alpha1.VmwareCloudFoundationMigrationSpec{
					FailureDomains: []configv1.VSpherePlatformFailureDomainSpec{},
				},
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kubeClient := kubefake.NewSimpleClientset()
			configClient := configfake.NewSimpleClientset()
			scheme := runtime.NewScheme()

			backupMgr := backup.NewBackupManager(scheme)
			apiextensionsClient := apiextensionsfake.NewSimpleClientset()
			machineClient := machinefake.NewSimpleClientset()
			dynamicClient := dynamicfake.NewSimpleDynamicClient(scheme)
			executor := phases.NewPhaseExecutor(kubeClient, configClient, apiextensionsClient, machineClient, dynamicClient, backupMgr, nil)

			phase := phases.NewMigrateCSIVolumesPhase(executor)

			err := phase.Validate(context.Background(), tt.migration)

			if tt.expectError && err == nil {
				t.Errorf("expected error but got none")
			}
			if !tt.expectError && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestMigrateCSIVolumesPhase_Execute_NoVolumes(t *testing.T) {
	// Create infrastructure
	infra := &configv1.Infrastructure{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cluster",
		},
		Spec: configv1.InfrastructureSpec{
			PlatformSpec: configv1.PlatformSpec{
				Type: configv1.VSpherePlatformType,
				VSphere: &configv1.VSpherePlatformSpec{
					VCenters: []configv1.VSpherePlatformVCenterSpec{
						{
							Server:      "source-vcenter.example.com",
							Datacenters: []string{"DC1"},
						},
					},
					FailureDomains: []configv1.VSpherePlatformFailureDomainSpec{
						{
							Name:   "source-fd",
							Server: "source-vcenter.example.com",
							Topology: configv1.VSpherePlatformTopology{
								Datacenter:     "DC1",
								ComputeCluster: "/DC1/host/cluster1",
								Datastore:      "/DC1/datastore/ds1",
								Networks:       []string{"VM Network"},
							},
						},
					},
				},
			},
		},
		Status: configv1.InfrastructureStatus{
			InfrastructureName: "test-cluster",
		},
	}

	kubeClient := kubefake.NewSimpleClientset()
	configClient := configfake.NewSimpleClientset(infra)
	scheme := runtime.NewScheme()

	backupMgr := backup.NewBackupManager(scheme)
	apiextensionsClient := apiextensionsfake.NewSimpleClientset()
	machineClient := machinefake.NewSimpleClientset()
	dynamicClient := dynamicfake.NewSimpleDynamicClient(scheme)
	executor := phases.NewPhaseExecutor(kubeClient, configClient, apiextensionsClient, machineClient, dynamicClient, backupMgr, nil)

	phase := phases.NewMigrateCSIVolumesPhase(executor)

	migration := &migrationv1alpha1.VmwareCloudFoundationMigration{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-migration",
			Namespace: "vmware-cloud-foundation-migration",
		},
		Spec: migrationv1alpha1.VmwareCloudFoundationMigrationSpec{
			FailureDomains: []configv1.VSpherePlatformFailureDomainSpec{
				{
					Name:   "target-fd",
					Server: "target-vcenter.example.com",
					Topology: configv1.VSpherePlatformTopology{
						Datacenter:     "DC2",
						ComputeCluster: "/DC2/host/cluster1",
						Datastore:      "/DC2/datastore/ds1",
						Networks:       []string{"VM Network"},
					},
				},
			},
		},
	}

	result, err := phase.Execute(context.Background(), migration)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if result.Status != migrationv1alpha1.PhaseStatusCompleted {
		t.Errorf("expected status Completed, got %s", result.Status)
	}

	if result.Message != "No vSphere CSI volumes to migrate" {
		t.Errorf("unexpected message: %s", result.Message)
	}
}

func TestMigrateCSIVolumesPhase_VolumeDiscovery(t *testing.T) {
	// Create infrastructure
	infra := &configv1.Infrastructure{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cluster",
		},
		Spec: configv1.InfrastructureSpec{
			PlatformSpec: configv1.PlatformSpec{
				Type: configv1.VSpherePlatformType,
				VSphere: &configv1.VSpherePlatformSpec{
					VCenters: []configv1.VSpherePlatformVCenterSpec{
						{
							Server:      "source-vcenter.example.com",
							Datacenters: []string{"DC1"},
						},
					},
					FailureDomains: []configv1.VSpherePlatformFailureDomainSpec{
						{
							Name:   "source-fd",
							Server: "source-vcenter.example.com",
							Topology: configv1.VSpherePlatformTopology{
								Datacenter:     "DC1",
								ComputeCluster: "/DC1/host/cluster1",
								Datastore:      "/DC1/datastore/ds1",
								Networks:       []string{"VM Network"},
							},
						},
					},
				},
			},
		},
		Status: configv1.InfrastructureStatus{
			InfrastructureName: "test-cluster",
		},
	}

	// Create CSI PVs
	pv1 := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: "pv-csi-1"},
		Spec: corev1.PersistentVolumeSpec{
			PersistentVolumeSource: corev1.PersistentVolumeSource{
				CSI: &corev1.CSIPersistentVolumeSource{
					Driver:       openshift.VSphereCSIDriver,
					VolumeHandle: "file://fcd-12345",
				},
			},
			ClaimRef: &corev1.ObjectReference{
				Name:      "test-pvc",
				Namespace: "default",
			},
		},
	}

	kubeClient := kubefake.NewSimpleClientset(pv1)
	configClient := configfake.NewSimpleClientset(infra)
	scheme := runtime.NewScheme()

	backupMgr := backup.NewBackupManager(scheme)
	apiextensionsClient := apiextensionsfake.NewSimpleClientset()
	machineClient := machinefake.NewSimpleClientset()
	dynamicClient := dynamicfake.NewSimpleDynamicClient(scheme)
	executor := phases.NewPhaseExecutor(kubeClient, configClient, apiextensionsClient, machineClient, dynamicClient, backupMgr, nil)

	phase := phases.NewMigrateCSIVolumesPhase(executor)

	migration := &migrationv1alpha1.VmwareCloudFoundationMigration{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-migration",
			Namespace: "vmware-cloud-foundation-migration",
		},
		Spec: migrationv1alpha1.VmwareCloudFoundationMigrationSpec{
			TargetVCenterCredentialsSecret: migrationv1alpha1.SecretReference{
				Name:      "target-creds",
				Namespace: "kube-system",
			},
			FailureDomains: []configv1.VSpherePlatformFailureDomainSpec{
				{
					Name:   "target-fd",
					Server: "target-vcenter.example.com",
					Topology: configv1.VSpherePlatformTopology{
						Datacenter:     "DC2",
						ComputeCluster: "/DC2/host/cluster1",
						Datastore:      "/DC2/datastore/ds1",
						Networks:       []string{"VM Network"},
					},
				},
			},
		},
	}

	// Execute phase - this will discover volumes but fail when trying to connect to vCenters
	// which is expected in unit tests
	_, _ = phase.Execute(context.Background(), migration)

	// Verify that volumes were discovered and added to status
	if migration.Status.CSIVolumeMigration == nil {
		t.Fatal("CSIVolumeMigration status should be initialized")
	}

	if migration.Status.CSIVolumeMigration.TotalVolumes != 1 {
		t.Errorf("expected 1 total volume, got %d", migration.Status.CSIVolumeMigration.TotalVolumes)
	}

	if len(migration.Status.CSIVolumeMigration.Volumes) != 1 {
		t.Fatalf("expected 1 volume state, got %d", len(migration.Status.CSIVolumeMigration.Volumes))
	}

	volState := migration.Status.CSIVolumeMigration.Volumes[0]
	if volState.PVName != "pv-csi-1" {
		t.Errorf("expected PV name 'pv-csi-1', got '%s'", volState.PVName)
	}

	if volState.PVCName != "test-pvc" {
		t.Errorf("expected PVC name 'test-pvc', got '%s'", volState.PVCName)
	}

	if volState.PVCNamespace != "default" {
		t.Errorf("expected PVC namespace 'default', got '%s'", volState.PVCNamespace)
	}
}

func TestCSIVolumeMigrationStatus_Initialization(t *testing.T) {
	status := &migrationv1alpha1.CSIVolumeMigrationStatus{
		TotalVolumes:    3,
		MigratedVolumes: 1,
		FailedVolumes:   0,
		Volumes: []migrationv1alpha1.PVMigrationState{
			{
				PVName:           "pv-1",
				SourceVolumePath: "file://fcd-1",
				Status:           "Complete",
			},
			{
				PVName:           "pv-2",
				SourceVolumePath: "file://fcd-2",
				Status:           "Pending",
			},
			{
				PVName:           "pv-3",
				SourceVolumePath: "file://fcd-3",
				Status:           "Pending",
			},
		},
	}

	if status.TotalVolumes != 3 {
		t.Errorf("expected 3 total volumes, got %d", status.TotalVolumes)
	}

	if status.MigratedVolumes != 1 {
		t.Errorf("expected 1 migrated volume, got %d", status.MigratedVolumes)
	}

	if len(status.Volumes) != 3 {
		t.Errorf("expected 3 volume states, got %d", len(status.Volumes))
	}
}

func TestPVMigrationState_ScaledDownResources(t *testing.T) {
	state := migrationv1alpha1.PVMigrationState{
		PVName:           "test-pv",
		PVCName:          "test-pvc",
		PVCNamespace:     "default",
		SourceVolumePath: "file://fcd-12345",
		Status:           "Quiesced",
		ScaledDownResources: []migrationv1alpha1.ScaledResource{
			{
				Kind:             "Deployment",
				Name:             "app-deployment",
				Namespace:        "default",
				OriginalReplicas: 3,
			},
			{
				Kind:             "StatefulSet",
				Name:             "db-statefulset",
				Namespace:        "default",
				OriginalReplicas: 1,
			},
		},
	}

	if len(state.ScaledDownResources) != 2 {
		t.Errorf("expected 2 scaled down resources, got %d", len(state.ScaledDownResources))
	}

	if state.ScaledDownResources[0].OriginalReplicas != 3 {
		t.Errorf("expected original replicas 3, got %d", state.ScaledDownResources[0].OriginalReplicas)
	}
}

func TestPVMigrationState_FailedStatus(t *testing.T) {
	// Test that failed volumes have proper status tracking
	state := migrationv1alpha1.PVMigrationState{
		PVName:           "failed-pv",
		PVCName:          "failed-pvc",
		PVCNamespace:     "default",
		SourceVolumePath: "file://fcd-failed",
		Status:           phases.PVStatusFailed,
		Message:          "VM relocation task failed: SSL thumbprint verification failed",
		ScaledDownResources: []migrationv1alpha1.ScaledResource{
			{
				Kind:             "Deployment",
				Name:             "app-deployment",
				Namespace:        "default",
				OriginalReplicas: 3,
			},
		},
	}

	if state.Status != phases.PVStatusFailed {
		t.Errorf("expected status Failed, got %s", state.Status)
	}

	if state.Message == "" {
		t.Error("expected error message to be set for failed status")
	}

	// Verify scaled down resources are preserved for failed volumes
	if len(state.ScaledDownResources) != 1 {
		t.Errorf("expected 1 scaled down resource preserved, got %d", len(state.ScaledDownResources))
	}
}

func TestCSIVolumeMigrationStatus_FailedVolumesTracking(t *testing.T) {
	status := &migrationv1alpha1.CSIVolumeMigrationStatus{
		TotalVolumes:    3,
		MigratedVolumes: 1,
		FailedVolumes:   1,
		Volumes: []migrationv1alpha1.PVMigrationState{
			{
				PVName:           "pv-1",
				SourceVolumePath: "file://fcd-1",
				Status:           phases.PVStatusComplete,
				Message:          "Volume migrated successfully",
			},
			{
				PVName:           "pv-2",
				SourceVolumePath: "file://fcd-2",
				Status:           phases.PVStatusFailed,
				Message:          "Failed to relocate volume: cross-vCenter vMotion failed",
				ScaledDownResources: []migrationv1alpha1.ScaledResource{
					{
						Kind:             "Deployment",
						Name:             "app-2",
						Namespace:        "default",
						OriginalReplicas: 2,
					},
				},
			},
			{
				PVName:           "pv-3",
				SourceVolumePath: "file://fcd-3",
				Status:           phases.PVStatusPending,
			},
		},
	}

	// Verify correct tracking
	if status.FailedVolumes != 1 {
		t.Errorf("expected 1 failed volume, got %d", status.FailedVolumes)
	}

	if status.MigratedVolumes != 1 {
		t.Errorf("expected 1 migrated volume, got %d", status.MigratedVolumes)
	}

	// Verify failed volume has scaled down resources preserved
	failedVol := status.Volumes[1]
	if failedVol.Status != phases.PVStatusFailed {
		t.Errorf("expected pv-2 to be Failed, got %s", failedVol.Status)
	}

	if len(failedVol.ScaledDownResources) == 0 {
		t.Error("expected failed volume to have scaled down resources preserved")
	}
}

func TestPVMigrationStatusConstants(t *testing.T) {
	// Verify all status constants are defined correctly
	statuses := []string{
		phases.PVStatusPending,
		phases.PVStatusQuiesced,
		phases.PVStatusRelocating,
		phases.PVStatusRelocated,
		phases.PVStatusRegistered,
		phases.PVStatusComplete,
		phases.PVStatusFailed,
	}

	for _, status := range statuses {
		if status == "" {
			t.Error("Status constant should not be empty")
		}
	}

	// Verify expected values
	if phases.PVStatusPending != "Pending" {
		t.Errorf("expected PVStatusPending to be 'Pending', got '%s'", phases.PVStatusPending)
	}
	if phases.PVStatusComplete != "Complete" {
		t.Errorf("expected PVStatusComplete to be 'Complete', got '%s'", phases.PVStatusComplete)
	}
	if phases.PVStatusFailed != "Failed" {
		t.Errorf("expected PVStatusFailed to be 'Failed', got '%s'", phases.PVStatusFailed)
	}
}
