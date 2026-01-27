package unit

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"

	configv1 "github.com/openshift/api/config/v1"
	configfake "github.com/openshift/client-go/config/clientset/versioned/fake"
	migrationv1alpha1 "github.com/openshift/vsphere-migration-controller/pkg/apis/migration/v1alpha1"
	"github.com/openshift/vsphere-migration-controller/pkg/backup"
	"github.com/openshift/vsphere-migration-controller/pkg/controller/phases"
)

func TestPreflightPhase_Validate(t *testing.T) {
	tests := []struct {
		name        string
		migration   *migrationv1alpha1.VSphereMigration
		expectError bool
	}{
		{
			name: "valid migration",
			migration: &migrationv1alpha1.VSphereMigration{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-migration",
					Namespace: "openshift-config",
				},
				Spec: migrationv1alpha1.VSphereMigrationSpec{
					TargetVCenterCredentialsSecret: migrationv1alpha1.SecretReference{
						Name:      "target-vcenter-creds",
						Namespace: "kube-system",
					},
					FailureDomains: []migrationv1alpha1.FailureDomain{
						{
							Name:   "fd1",
							Region: "us-east",
							Zone:   "us-east-1a",
							Server: "new-vcenter.example.com",
							Topology: migrationv1alpha1.FailureDomainTopology{
								Datacenter:     "DC2",
								ComputeCluster: "/DC2/host/cluster1",
								Datastore:      "/DC2/datastore/ds1",
								Networks:       []string{"VM Network"},
							},
						},
					},
				},
			},
			expectError: false,
		},
		{
			name: "missing target credentials secret",
			migration: &migrationv1alpha1.VSphereMigration{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-migration",
					Namespace: "openshift-config",
				},
				Spec: migrationv1alpha1.VSphereMigrationSpec{
					TargetVCenterCredentialsSecret: migrationv1alpha1.SecretReference{
						Name: "",
					},
					FailureDomains: []migrationv1alpha1.FailureDomain{
						{
							Name:   "fd1",
							Server: "new-vcenter.example.com",
						},
					},
				},
			},
			expectError: true,
		},
		{
			name: "missing failure domains",
			migration: &migrationv1alpha1.VSphereMigration{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-migration",
					Namespace: "openshift-config",
				},
				Spec: migrationv1alpha1.VSphereMigrationSpec{
					TargetVCenterCredentialsSecret: migrationv1alpha1.SecretReference{
						Name:      "target-vcenter-creds",
						Namespace: "kube-system",
					},
					FailureDomains: []migrationv1alpha1.FailureDomain{},
				},
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create fake clients
			kubeClient := fake.NewSimpleClientset()
			configClient := configfake.NewSimpleClientset()
			scheme := runtime.NewScheme()

			// Create executor
			backupMgr := backup.NewBackupManager(scheme)
			executor := phases.NewPhaseExecutor(kubeClient, configClient, backupMgr, nil)

			// Create phase
			phase := phases.NewPreflightPhase(executor)

			// Test validation
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

func TestBackupPhase_Name(t *testing.T) {
	kubeClient := fake.NewSimpleClientset()
	configClient := configfake.NewSimpleClientset()
	scheme := runtime.NewScheme()

	backupMgr := backup.NewBackupManager(scheme)
	executor := phases.NewPhaseExecutor(kubeClient, configClient, backupMgr, nil)

	phase := phases.NewBackupPhase(executor)

	if phase.Name() != migrationv1alpha1.PhaseBackup {
		t.Errorf("expected phase name %s, got %s", migrationv1alpha1.PhaseBackup, phase.Name())
	}
}

func TestDisableCVOPhase_Execute(t *testing.T) {
	// Create CVO deployment
	replicas := int32(1)
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cluster-version-operator",
			Namespace: "openshift-cluster-version",
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
		},
	}

	kubeClient := fake.NewSimpleClientset(deployment)
	configClient := configfake.NewSimpleClientset()
	scheme := runtime.NewScheme()

	backupMgr := backup.NewBackupManager(scheme)
	executor := phases.NewPhaseExecutor(kubeClient, configClient, backupMgr, nil)

	phase := phases.NewDisableCVOPhase(executor)

	migration := &migrationv1alpha1.VSphereMigration{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-migration",
			Namespace: "openshift-config",
		},
	}

	// Execute phase
	result, err := phase.Execute(context.Background(), migration)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if result.Status != migrationv1alpha1.PhaseStatusCompleted {
		t.Errorf("expected status Completed, got %s", result.Status)
	}

	// Verify deployment was scaled down
	updatedDeployment, err := kubeClient.AppsV1().Deployments("openshift-cluster-version").Get(context.Background(), "cluster-version-operator", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Failed to get deployment: %v", err)
	}

	if *updatedDeployment.Spec.Replicas != 0 {
		t.Errorf("expected replicas to be 0, got %d", *updatedDeployment.Spec.Replicas)
	}
}

func TestUpdateSecretsPhase_Validate(t *testing.T) {
	tests := []struct {
		name        string
		migration   *migrationv1alpha1.VSphereMigration
		expectError bool
	}{
		{
			name: "valid migration",
			migration: &migrationv1alpha1.VSphereMigration{
				Spec: migrationv1alpha1.VSphereMigrationSpec{
					TargetVCenterCredentialsSecret: migrationv1alpha1.SecretReference{
						Name:      "target-vcenter-creds",
						Namespace: "kube-system",
					},
					FailureDomains: []migrationv1alpha1.FailureDomain{
						{
							Name:   "fd1",
							Server: "new-vcenter.example.com",
						},
					},
				},
			},
			expectError: false,
		},
		{
			name: "missing credentials secret",
			migration: &migrationv1alpha1.VSphereMigration{
				Spec: migrationv1alpha1.VSphereMigrationSpec{
					TargetVCenterCredentialsSecret: migrationv1alpha1.SecretReference{
						Name: "",
					},
					FailureDomains: []migrationv1alpha1.FailureDomain{
						{
							Name:   "fd1",
							Server: "new-vcenter.example.com",
						},
					},
				},
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kubeClient := fake.NewSimpleClientset()
			configClient := configfake.NewSimpleClientset()
			scheme := runtime.NewScheme()

			backupMgr := backup.NewBackupManager(scheme)
			executor := phases.NewPhaseExecutor(kubeClient, configClient, backupMgr, nil)

			phase := phases.NewUpdateSecretsPhase(executor)

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

func TestCreateTagsPhase_Validate(t *testing.T) {
	tests := []struct {
		name        string
		migration   *migrationv1alpha1.VSphereMigration
		expectError bool
	}{
		{
			name: "valid migration with failure domains",
			migration: &migrationv1alpha1.VSphereMigration{
				Spec: migrationv1alpha1.VSphereMigrationSpec{
					FailureDomains: []migrationv1alpha1.FailureDomain{
						{
							Name:   "fd1",
							Region: "us-east",
							Zone:   "us-east-1a",
						},
					},
				},
			},
			expectError: false,
		},
		{
			name: "missing failure domains",
			migration: &migrationv1alpha1.VSphereMigration{
				Spec: migrationv1alpha1.VSphereMigrationSpec{
					FailureDomains: []migrationv1alpha1.FailureDomain{},
				},
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kubeClient := fake.NewSimpleClientset()
			configClient := configfake.NewSimpleClientset()
			scheme := runtime.NewScheme()

			backupMgr := backup.NewBackupManager(scheme)
			executor := phases.NewPhaseExecutor(kubeClient, configClient, backupMgr, nil)

			phase := phases.NewCreateTagsPhase(executor)

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

func TestAllPhases_HaveCorrectNames(t *testing.T) {
	kubeClient := fake.NewSimpleClientset()
	configClient := configfake.NewSimpleClientset()
	scheme := runtime.NewScheme()

	backupMgr := backup.NewBackupManager(scheme)
	executor := phases.NewPhaseExecutor(kubeClient, configClient, backupMgr, nil)

	tests := []struct {
		phase        phases.Phase
		expectedName migrationv1alpha1.MigrationPhase
	}{
		{phases.NewPreflightPhase(executor), migrationv1alpha1.PhasePreflight},
		{phases.NewBackupPhase(executor), migrationv1alpha1.PhaseBackup},
		{phases.NewDisableCVOPhase(executor), migrationv1alpha1.PhaseDisableCVO},
		{phases.NewUpdateSecretsPhase(executor), migrationv1alpha1.PhaseUpdateSecrets},
		{phases.NewCreateTagsPhase(executor), migrationv1alpha1.PhaseCreateTags},
		{phases.NewCreateFolderPhase(executor), migrationv1alpha1.PhaseCreateFolder},
		{phases.NewUpdateInfrastructurePhase(executor), migrationv1alpha1.PhaseUpdateInfrastructure},
		{phases.NewUpdateConfigPhase(executor), migrationv1alpha1.PhaseUpdateConfig},
		{phases.NewRestartPodsPhase(executor), migrationv1alpha1.PhaseRestartPods},
		{phases.NewMonitorHealthPhase(executor), migrationv1alpha1.PhaseMonitorHealth},
		{phases.NewCreateWorkersPhase(executor), migrationv1alpha1.PhaseCreateWorkers},
		{phases.NewRecreateCPMSPhase(executor), migrationv1alpha1.PhaseRecreateCPMS},
		{phases.NewScaleOldMachinesPhase(executor), migrationv1alpha1.PhaseScaleOldMachines},
		{phases.NewCleanupPhase(executor), migrationv1alpha1.PhaseCleanup},
		{phases.NewVerifyPhase(executor), migrationv1alpha1.PhaseVerify},
	}

	for _, tt := range tests {
		t.Run(string(tt.expectedName), func(t *testing.T) {
			if tt.phase.Name() != tt.expectedName {
				t.Errorf("expected phase name %s, got %s", tt.expectedName, tt.phase.Name())
			}
		})
	}
}

func TestUpdateInfrastructurePhase_Execute(t *testing.T) {
	// Create Infrastructure object
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
							Server:      "old-vcenter.example.com",
							Datacenters: []string{"DC1"},
						},
					},
					FailureDomains: []configv1.VSpherePlatformFailureDomainSpec{},
				},
			},
		},
	}

	configClient := configfake.NewSimpleClientset(infra)
	kubeClient := fake.NewSimpleClientset()
	scheme := runtime.NewScheme()

	backupMgr := backup.NewBackupManager(scheme)
	executor := phases.NewPhaseExecutor(kubeClient, configClient, backupMgr, nil)

	phase := phases.NewUpdateInfrastructurePhase(executor)

	migration := &migrationv1alpha1.VSphereMigration{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-migration",
			Namespace: "openshift-config",
		},
		Spec: migrationv1alpha1.VSphereMigrationSpec{
			TargetVCenterCredentialsSecret: migrationv1alpha1.SecretReference{
				Name:      "target-vcenter-creds",
				Namespace: "kube-system",
			},
			FailureDomains: []migrationv1alpha1.FailureDomain{
				{
					Name:   "fd1",
					Region: "us-east",
					Zone:   "us-east-1a",
					Server: "new-vcenter.example.com",
					Topology: migrationv1alpha1.FailureDomainTopology{
						Datacenter:     "DC2",
						ComputeCluster: "/DC2/host/cluster1",
						Datastore:      "/DC2/datastore/ds1",
						Networks:       []string{"VM Network"},
					},
				},
			},
		},
	}

	// Execute phase
	result, err := phase.Execute(context.Background(), migration)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if result.Status != migrationv1alpha1.PhaseStatusCompleted {
		t.Errorf("expected status Completed, got %s", result.Status)
	}

	// Verify infrastructure was updated
	updatedInfra, err := configClient.ConfigV1().Infrastructures().Get(context.Background(), "cluster", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Failed to get infrastructure: %v", err)
	}

	if len(updatedInfra.Spec.PlatformSpec.VSphere.VCenters) != 2 {
		t.Errorf("expected 2 vCenters, got %d", len(updatedInfra.Spec.PlatformSpec.VSphere.VCenters))
	}

	if len(updatedInfra.Spec.PlatformSpec.VSphere.FailureDomains) != 1 {
		t.Errorf("expected 1 failure domain, got %d", len(updatedInfra.Spec.PlatformSpec.VSphere.FailureDomains))
	}
}
