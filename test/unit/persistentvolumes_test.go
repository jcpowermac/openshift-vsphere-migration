package unit

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubefake "k8s.io/client-go/kubernetes/fake"

	"github.com/openshift/vmware-cloud-foundation-migration/pkg/openshift"
)

func TestListVSphereCSIVolumes(t *testing.T) {
	tests := []struct {
		name           string
		pvs            []corev1.PersistentVolume
		expectedCount  int
	}{
		{
			name: "finds vSphere CSI volumes",
			pvs: []corev1.PersistentVolume{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "pv-csi-1"},
					Spec: corev1.PersistentVolumeSpec{
						Capacity: corev1.ResourceList{
							corev1.ResourceStorage: resource.MustParse("10Gi"),
						},
						PersistentVolumeSource: corev1.PersistentVolumeSource{
							CSI: &corev1.CSIPersistentVolumeSource{
								Driver:       openshift.VSphereCSIDriver,
								VolumeHandle: "file://12345-abcde",
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{Name: "pv-csi-2"},
					Spec: corev1.PersistentVolumeSpec{
						Capacity: corev1.ResourceList{
							corev1.ResourceStorage: resource.MustParse("20Gi"),
						},
						PersistentVolumeSource: corev1.PersistentVolumeSource{
							CSI: &corev1.CSIPersistentVolumeSource{
								Driver:       openshift.VSphereCSIDriver,
								VolumeHandle: "file://67890-fghij",
							},
						},
					},
				},
			},
			expectedCount: 2,
		},
		{
			name: "ignores non-CSI volumes",
			pvs: []corev1.PersistentVolume{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "pv-csi-1"},
					Spec: corev1.PersistentVolumeSpec{
						PersistentVolumeSource: corev1.PersistentVolumeSource{
							CSI: &corev1.CSIPersistentVolumeSource{
								Driver:       openshift.VSphereCSIDriver,
								VolumeHandle: "file://12345-abcde",
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{Name: "pv-nfs"},
					Spec: corev1.PersistentVolumeSpec{
						PersistentVolumeSource: corev1.PersistentVolumeSource{
							NFS: &corev1.NFSVolumeSource{
								Server: "nfs.example.com",
								Path:   "/exports/data",
							},
						},
					},
				},
			},
			expectedCount: 1,
		},
		{
			name: "ignores non-vSphere CSI drivers",
			pvs: []corev1.PersistentVolume{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "pv-csi-vsphere"},
					Spec: corev1.PersistentVolumeSpec{
						PersistentVolumeSource: corev1.PersistentVolumeSource{
							CSI: &corev1.CSIPersistentVolumeSource{
								Driver:       openshift.VSphereCSIDriver,
								VolumeHandle: "file://12345-abcde",
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{Name: "pv-csi-ebs"},
					Spec: corev1.PersistentVolumeSpec{
						PersistentVolumeSource: corev1.PersistentVolumeSource{
							CSI: &corev1.CSIPersistentVolumeSource{
								Driver:       "ebs.csi.aws.com",
								VolumeHandle: "vol-12345",
							},
						},
					},
				},
			},
			expectedCount: 1,
		},
		{
			name:          "returns empty for no volumes",
			pvs:           []corev1.PersistentVolume{},
			expectedCount: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create fake client with PVs
			var objects []interface{}
			for i := range tt.pvs {
				objects = append(objects, &tt.pvs[i])
			}

			kubeClient := kubefake.NewSimpleClientset()
			for i := range tt.pvs {
				_, err := kubeClient.CoreV1().PersistentVolumes().Create(context.Background(), &tt.pvs[i], metav1.CreateOptions{})
				if err != nil {
					t.Fatalf("Failed to create PV: %v", err)
				}
			}

			pvManager := openshift.NewPersistentVolumeManager(kubeClient)

			csiPVs, err := pvManager.ListVSphereCSIVolumes(context.Background())
			if err != nil {
				t.Fatalf("ListVSphereCSIVolumes failed: %v", err)
			}

			if len(csiPVs) != tt.expectedCount {
				t.Errorf("expected %d volumes, got %d", tt.expectedCount, len(csiPVs))
			}
		})
	}
}

func TestUpdatePVVolumeHandle(t *testing.T) {
	// Create PV with CSI source
	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: "pv-test"},
		Spec: corev1.PersistentVolumeSpec{
			PersistentVolumeSource: corev1.PersistentVolumeSource{
				CSI: &corev1.CSIPersistentVolumeSource{
					Driver:       openshift.VSphereCSIDriver,
					VolumeHandle: "file://old-id-12345",
				},
			},
		},
	}

	kubeClient := kubefake.NewSimpleClientset(pv)
	pvManager := openshift.NewPersistentVolumeManager(kubeClient)

	newHandle := "file://new-id-67890"
	err := pvManager.UpdatePVVolumeHandle(context.Background(), "pv-test", newHandle)
	if err != nil {
		t.Fatalf("UpdatePVVolumeHandle failed: %v", err)
	}

	// Verify update
	updatedPV, err := kubeClient.CoreV1().PersistentVolumes().Get(context.Background(), "pv-test", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Failed to get PV: %v", err)
	}

	if updatedPV.Spec.CSI.VolumeHandle != newHandle {
		t.Errorf("expected volumeHandle %s, got %s", newHandle, updatedPV.Spec.CSI.VolumeHandle)
	}
}

func TestFindPodsUsingPVC(t *testing.T) {
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pvc",
			Namespace: "default",
		},
	}

	pods := []corev1.Pod{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "pod-using-pvc",
				Namespace: "default",
			},
			Spec: corev1.PodSpec{
				Volumes: []corev1.Volume{
					{
						Name: "data",
						VolumeSource: corev1.VolumeSource{
							PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
								ClaimName: "test-pvc",
							},
						},
					},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "pod-not-using-pvc",
				Namespace: "default",
			},
			Spec: corev1.PodSpec{
				Volumes: []corev1.Volume{
					{
						Name: "config",
						VolumeSource: corev1.VolumeSource{
							ConfigMap: &corev1.ConfigMapVolumeSource{
								LocalObjectReference: corev1.LocalObjectReference{
									Name: "config-map",
								},
							},
						},
					},
				},
			},
		},
	}

	kubeClient := kubefake.NewSimpleClientset(pvc, &pods[0], &pods[1])
	pvManager := openshift.NewPersistentVolumeManager(kubeClient)

	usingPods, err := pvManager.FindPodsUsingPVC(context.Background(), "default", "test-pvc")
	if err != nil {
		t.Fatalf("FindPodsUsingPVC failed: %v", err)
	}

	if len(usingPods) != 1 {
		t.Errorf("expected 1 pod using PVC, got %d", len(usingPods))
	}

	if len(usingPods) > 0 && usingPods[0].Name != "pod-using-pvc" {
		t.Errorf("expected pod 'pod-using-pvc', got '%s'", usingPods[0].Name)
	}
}

func TestParseVSphereVolumeHandle(t *testing.T) {
	tests := []struct {
		name        string
		handle      string
		expectedID  string
	}{
		{
			name:       "file:// prefix",
			handle:     "file://12345-abcde-67890",
			expectedID: "12345-abcde-67890",
		},
		{
			name:       "no prefix",
			handle:     "12345-abcde-67890",
			expectedID: "12345-abcde-67890",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fcdID, err := openshift.ParseVSphereVolumeHandle(tt.handle)
			if err != nil {
				t.Fatalf("ParseVSphereVolumeHandle failed: %v", err)
			}

			if fcdID != tt.expectedID {
				t.Errorf("expected FCD ID %s, got %s", tt.expectedID, fcdID)
			}
		})
	}
}

func TestBuildVSphereVolumeHandle(t *testing.T) {
	fcdID := "12345-abcde-67890"
	expected := "file://12345-abcde-67890"

	handle := openshift.BuildVSphereVolumeHandle(fcdID)
	if handle != expected {
		t.Errorf("expected handle %s, got %s", expected, handle)
	}
}
