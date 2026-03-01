/*
Copyright 2026.

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

package v1alpha1

import (
	"testing"

	cortexv1alpha1 "github.com/cortexproject/cortex/operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func ptr[T any](v T) *T {
	return &v
}

func TestDefaulting(t *testing.T) {
	cortex := &cortexv1alpha1.Cortex{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test",
		},
		Spec: cortexv1alpha1.CortexSpec{
			Image: cortexv1alpha1.ImageSpec{
				Tag: "v1.17.0",
			},
			Storage: cortexv1alpha1.StorageSpec{
				Backend: cortexv1alpha1.StorageBackendS3,
				S3:      &cortexv1alpha1.S3StorageSpec{BucketName: "test"},
			},
		},
	}

	defaulter := &CortexCustomDefaulter{}
	if err := defaulter.Default(nil, cortex); err != nil {
		t.Fatalf("defaulting failed: %v", err)
	}

	// Image defaults.
	if cortex.Spec.Image.Repository != "quay.io/cortexproject/cortex" {
		t.Errorf("expected default repository, got %s", cortex.Spec.Image.Repository)
	}
	if cortex.Spec.Image.PullPolicy != corev1.PullIfNotPresent {
		t.Errorf("expected default pull policy, got %s", cortex.Spec.Image.PullPolicy)
	}

	// Auth default.
	if cortex.Spec.AuthEnabled == nil || !*cortex.Spec.AuthEnabled {
		t.Error("expected authEnabled to default to true")
	}

	// Ring defaults.
	if cortex.Spec.Ring == nil {
		t.Fatal("expected ring to be defaulted")
	}
	if cortex.Spec.Ring.GetReplicationFactor() != 3 {
		t.Errorf("expected replicationFactor=3, got %d", cortex.Spec.Ring.GetReplicationFactor())
	}
	if cortex.Spec.Ring.GetNumTokens() != 512 {
		t.Errorf("expected numTokens=512, got %d", cortex.Spec.Ring.GetNumTokens())
	}

	// Component defaults.
	if cortex.Spec.Distributor == nil || cortex.Spec.Distributor.GetReplicas() != 1 {
		t.Error("expected distributor replicas to default to 1")
	}
	if cortex.Spec.Ingester == nil || cortex.Spec.Ingester.GetReplicas() != 1 {
		t.Error("expected ingester replicas to default to 1")
	}
	if cortex.Spec.Ingester.GetTerminationGracePeriodSeconds() != 2400 {
		t.Errorf("expected ingester terminationGracePeriod=2400, got %d", cortex.Spec.Ingester.GetTerminationGracePeriodSeconds())
	}

	// Memberlist defaults.
	if cortex.Spec.Memberlist == nil || cortex.Spec.Memberlist.GetBindPort() != 7946 {
		t.Error("expected memberlist bindPort to default to 7946")
	}

	// Store gateway defaults.
	if cortex.Spec.StoreGateway == nil || !cortex.Spec.StoreGateway.GetShardingEnabled() {
		t.Error("expected store-gateway sharding to default to true")
	}
}

func TestValidation_MissingImageTag(t *testing.T) {
	cortex := &cortexv1alpha1.Cortex{
		Spec: cortexv1alpha1.CortexSpec{
			Image: cortexv1alpha1.ImageSpec{},
			Storage: cortexv1alpha1.StorageSpec{
				Backend: cortexv1alpha1.StorageBackendS3,
				S3:      &cortexv1alpha1.S3StorageSpec{BucketName: "test"},
			},
		},
	}

	_, err := validateCortex(cortex)
	if err == nil {
		t.Error("expected validation error for missing image tag")
	}
}

func TestValidation_MissingS3Bucket(t *testing.T) {
	cortex := &cortexv1alpha1.Cortex{
		Spec: cortexv1alpha1.CortexSpec{
			Image: cortexv1alpha1.ImageSpec{Tag: "v1.17.0"},
			Storage: cortexv1alpha1.StorageSpec{
				Backend: cortexv1alpha1.StorageBackendS3,
			},
		},
	}

	_, err := validateCortex(cortex)
	if err == nil {
		t.Error("expected validation error for missing S3 bucket")
	}
}

func TestValidation_MissingGCSBucket(t *testing.T) {
	cortex := &cortexv1alpha1.Cortex{
		Spec: cortexv1alpha1.CortexSpec{
			Image: cortexv1alpha1.ImageSpec{Tag: "v1.17.0"},
			Storage: cortexv1alpha1.StorageSpec{
				Backend: cortexv1alpha1.StorageBackendGCS,
			},
		},
	}

	_, err := validateCortex(cortex)
	if err == nil {
		t.Error("expected validation error for missing GCS bucket")
	}
}

func TestValidation_MissingAzureContainer(t *testing.T) {
	cortex := &cortexv1alpha1.Cortex{
		Spec: cortexv1alpha1.CortexSpec{
			Image: cortexv1alpha1.ImageSpec{Tag: "v1.17.0"},
			Storage: cortexv1alpha1.StorageSpec{
				Backend: cortexv1alpha1.StorageBackendAzure,
			},
		},
	}

	_, err := validateCortex(cortex)
	if err == nil {
		t.Error("expected validation error for missing Azure container")
	}
}

func TestValidation_MissingSwiftContainer(t *testing.T) {
	cortex := &cortexv1alpha1.Cortex{
		Spec: cortexv1alpha1.CortexSpec{
			Image: cortexv1alpha1.ImageSpec{Tag: "v1.17.0"},
			Storage: cortexv1alpha1.StorageSpec{
				Backend: cortexv1alpha1.StorageBackendSwift,
			},
		},
	}

	_, err := validateCortex(cortex)
	if err == nil {
		t.Error("expected validation error for missing Swift container")
	}
}

func TestValidation_ValidSpec(t *testing.T) {
	cortex := &cortexv1alpha1.Cortex{
		Spec: cortexv1alpha1.CortexSpec{
			Image: cortexv1alpha1.ImageSpec{Tag: "v1.17.0"},
			Storage: cortexv1alpha1.StorageSpec{
				Backend: cortexv1alpha1.StorageBackendS3,
				S3:      &cortexv1alpha1.S3StorageSpec{BucketName: "my-bucket"},
			},
		},
	}

	_, err := validateCortex(cortex)
	if err != nil {
		t.Errorf("expected valid spec, got error: %v", err)
	}
}

func TestValidation_IngesterReplicasBelowReplicationFactor(t *testing.T) {
	cortex := &cortexv1alpha1.Cortex{
		Spec: cortexv1alpha1.CortexSpec{
			Image: cortexv1alpha1.ImageSpec{Tag: "v1.17.0"},
			Storage: cortexv1alpha1.StorageSpec{
				Backend: cortexv1alpha1.StorageBackendS3,
				S3:      &cortexv1alpha1.S3StorageSpec{BucketName: "test"},
			},
			Ring: &cortexv1alpha1.RingSpec{
				ReplicationFactor: ptr(int32(3)),
			},
			Ingester: &cortexv1alpha1.IngesterComponentSpec{
				ComponentSpec: cortexv1alpha1.ComponentSpec{
					Replicas: ptr(int32(2)),
				},
			},
		},
	}

	_, err := validateCortex(cortex)
	if err == nil {
		t.Error("expected validation error when ingester replicas < replication factor")
	}
}

func TestValidation_ScaleDownWarning(t *testing.T) {
	validator := &CortexCustomValidator{}

	oldCortex := &cortexv1alpha1.Cortex{
		Spec: cortexv1alpha1.CortexSpec{
			Image: cortexv1alpha1.ImageSpec{Tag: "v1.17.0"},
			Storage: cortexv1alpha1.StorageSpec{
				Backend: cortexv1alpha1.StorageBackendS3,
				S3:      &cortexv1alpha1.S3StorageSpec{BucketName: "test"},
			},
			Ingester: &cortexv1alpha1.IngesterComponentSpec{
				ComponentSpec: cortexv1alpha1.ComponentSpec{
					Replicas: ptr(int32(5)),
				},
			},
		},
	}

	newCortex := &cortexv1alpha1.Cortex{
		Spec: cortexv1alpha1.CortexSpec{
			Image: cortexv1alpha1.ImageSpec{Tag: "v1.17.0"},
			Storage: cortexv1alpha1.StorageSpec{
				Backend: cortexv1alpha1.StorageBackendS3,
				S3:      &cortexv1alpha1.S3StorageSpec{BucketName: "test"},
			},
			Ingester: &cortexv1alpha1.IngesterComponentSpec{
				ComponentSpec: cortexv1alpha1.ComponentSpec{
					Replicas: ptr(int32(3)),
				},
			},
		},
	}

	warnings, err := validator.ValidateUpdate(nil, oldCortex, newCortex)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if len(warnings) == 0 {
		t.Error("expected a warning about ingester scale-down")
	}
}
