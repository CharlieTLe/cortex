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
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	cortexv1alpha1 "github.com/cortexproject/cortex/operator/api/v1alpha1"
)

var cortexlog = logf.Log.WithName("cortex-resource")

// SetupCortexWebhookWithManager registers the webhook for Cortex in the manager.
func SetupCortexWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).For(&cortexv1alpha1.Cortex{}).
		WithValidator(&CortexCustomValidator{}).
		WithDefaulter(&CortexCustomDefaulter{}).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-cortex-cortex-io-v1alpha1-cortex,mutating=true,failurePolicy=fail,sideEffects=None,groups=cortex.cortex.io,resources=cortexes,verbs=create;update,versions=v1alpha1,name=mcortex-v1alpha1.kb.io,admissionReviewVersions=v1

// CortexCustomDefaulter sets default values on the Cortex resource.
// +kubebuilder:object:generate=false
type CortexCustomDefaulter struct{}

var _ webhook.CustomDefaulter = &CortexCustomDefaulter{}

// Default implements webhook.CustomDefaulter.
func (d *CortexCustomDefaulter) Default(ctx context.Context, obj runtime.Object) error {
	cortex, ok := obj.(*cortexv1alpha1.Cortex)
	if !ok {
		return fmt.Errorf("expected a Cortex object but got %T", obj)
	}
	cortexlog.Info("Defaulting for Cortex", "name", cortex.GetName())

	// Image defaults.
	if cortex.Spec.Image.Repository == "" {
		cortex.Spec.Image.Repository = "quay.io/cortexproject/cortex"
	}
	if cortex.Spec.Image.PullPolicy == "" {
		cortex.Spec.Image.PullPolicy = corev1.PullIfNotPresent
	}

	// Auth default.
	if cortex.Spec.AuthEnabled == nil {
		t := true
		cortex.Spec.AuthEnabled = &t
	}

	// Ring defaults.
	if cortex.Spec.Ring == nil {
		cortex.Spec.Ring = &cortexv1alpha1.RingSpec{}
	}
	if cortex.Spec.Ring.KVStore == "" {
		cortex.Spec.Ring.KVStore = "memberlist"
	}
	if cortex.Spec.Ring.ReplicationFactor == nil {
		rf := int32(3)
		cortex.Spec.Ring.ReplicationFactor = &rf
	}
	if cortex.Spec.Ring.NumTokens == nil {
		nt := int32(512)
		cortex.Spec.Ring.NumTokens = &nt
	}

	// Memberlist defaults.
	if cortex.Spec.Memberlist == nil {
		cortex.Spec.Memberlist = &cortexv1alpha1.MemberlistSpec{}
	}
	if cortex.Spec.Memberlist.BindPort == nil {
		bp := int32(7946)
		cortex.Spec.Memberlist.BindPort = &bp
	}
	if cortex.Spec.Memberlist.AbortIfJoinFails == nil {
		f := false
		cortex.Spec.Memberlist.AbortIfJoinFails = &f
	}

	// Component replica defaults.
	defaultReplicas := func(spec **cortexv1alpha1.ComponentSpec) {
		if *spec == nil {
			*spec = &cortexv1alpha1.ComponentSpec{}
		}
		if (*spec).Replicas == nil {
			r := int32(1)
			(*spec).Replicas = &r
		}
	}

	defaultReplicas(&cortex.Spec.Distributor)
	defaultReplicas(&cortex.Spec.Querier)
	defaultReplicas(&cortex.Spec.QueryFrontend)

	// Compactor defaults.
	if cortex.Spec.Compactor == nil {
		cortex.Spec.Compactor = &cortexv1alpha1.CompactorComponentSpec{}
	}
	if cortex.Spec.Compactor.Replicas == nil {
		r := int32(1)
		cortex.Spec.Compactor.Replicas = &r
	}

	// Ingester defaults.
	if cortex.Spec.Ingester == nil {
		cortex.Spec.Ingester = &cortexv1alpha1.IngesterComponentSpec{}
	}
	if cortex.Spec.Ingester.Replicas == nil {
		r := int32(1)
		cortex.Spec.Ingester.Replicas = &r
	}
	if cortex.Spec.Ingester.TerminationGracePeriodSeconds == nil {
		t := int64(2400)
		cortex.Spec.Ingester.TerminationGracePeriodSeconds = &t
	}

	// Store Gateway defaults.
	if cortex.Spec.StoreGateway == nil {
		cortex.Spec.StoreGateway = &cortexv1alpha1.StoreGatewaySpec{}
	}
	if cortex.Spec.StoreGateway.Replicas == nil {
		r := int32(1)
		cortex.Spec.StoreGateway.Replicas = &r
	}
	if cortex.Spec.StoreGateway.ShardingEnabled == nil {
		t := true
		cortex.Spec.StoreGateway.ShardingEnabled = &t
	}

	// Zone awareness defaults.
	if cortex.Spec.ZoneAwareness != nil && cortex.Spec.ZoneAwareness.TopologyKey == "" {
		cortex.Spec.ZoneAwareness.TopologyKey = "topology.kubernetes.io/zone"
	}

	return nil
}

// +kubebuilder:webhook:path=/validate-cortex-cortex-io-v1alpha1-cortex,mutating=false,failurePolicy=fail,sideEffects=None,groups=cortex.cortex.io,resources=cortexes,verbs=create;update,versions=v1alpha1,name=vcortex-v1alpha1.kb.io,admissionReviewVersions=v1

// CortexCustomValidator validates the Cortex resource.
// +kubebuilder:object:generate=false
type CortexCustomValidator struct{}

var _ webhook.CustomValidator = &CortexCustomValidator{}

// ValidateCreate implements webhook.CustomValidator.
func (v *CortexCustomValidator) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	cortex, ok := obj.(*cortexv1alpha1.Cortex)
	if !ok {
		return nil, fmt.Errorf("expected a Cortex object but got %T", obj)
	}
	cortexlog.Info("Validation for Cortex upon creation", "name", cortex.GetName())

	return validateCortex(cortex)
}

// ValidateUpdate implements webhook.CustomValidator.
func (v *CortexCustomValidator) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	cortex, ok := newObj.(*cortexv1alpha1.Cortex)
	if !ok {
		return nil, fmt.Errorf("expected a Cortex object for the newObj but got %T", newObj)
	}
	cortexlog.Info("Validation for Cortex upon update", "name", cortex.GetName())

	warnings, err := validateCortex(cortex)

	// Warn on ingester scale-down.
	oldCortex, ok := oldObj.(*cortexv1alpha1.Cortex)
	if ok && oldCortex.Spec.Ingester != nil && cortex.Spec.Ingester != nil {
		oldReplicas := oldCortex.Spec.Ingester.GetReplicas()
		newReplicas := cortex.Spec.Ingester.GetReplicas()
		if newReplicas < oldReplicas {
			warnings = append(warnings, fmt.Sprintf("scaling down ingesters from %d to %d — ensure ring has stabilized before proceeding", oldReplicas, newReplicas))
		}
	}

	// Warn on zone awareness toggle.
	if ok {
		oldZA := oldCortex.Spec.IsZoneAwarenessEnabled()
		newZA := cortex.Spec.IsZoneAwarenessEnabled()
		if oldZA && !newZA {
			warnings = append(warnings, "disabling zone awareness — existing per-zone StatefulSets will be replaced with a single StatefulSet")
		} else if !oldZA && newZA {
			warnings = append(warnings, "enabling zone awareness — existing StatefulSet will be replaced with per-zone StatefulSets")
		}
	}

	return warnings, err
}

// ValidateDelete implements webhook.CustomValidator.
func (v *CortexCustomValidator) ValidateDelete(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

// validateCortex performs validation on the Cortex spec.
func validateCortex(cortex *cortexv1alpha1.Cortex) (admission.Warnings, error) {
	var warnings admission.Warnings

	// Image tag is required.
	if cortex.Spec.Image.Tag == "" {
		return warnings, fmt.Errorf("spec.image.tag is required")
	}

	// Storage backend validation.
	switch cortex.Spec.Storage.Backend {
	case cortexv1alpha1.StorageBackendS3:
		if cortex.Spec.Storage.S3 == nil || cortex.Spec.Storage.S3.BucketName == "" {
			return warnings, fmt.Errorf("spec.storage.s3.bucketName is required when backend is s3")
		}
	case cortexv1alpha1.StorageBackendGCS:
		if cortex.Spec.Storage.GCS == nil || cortex.Spec.Storage.GCS.BucketName == "" {
			return warnings, fmt.Errorf("spec.storage.gcs.bucketName is required when backend is gcs")
		}
	case cortexv1alpha1.StorageBackendAzure:
		if cortex.Spec.Storage.Azure == nil || cortex.Spec.Storage.Azure.ContainerName == "" {
			return warnings, fmt.Errorf("spec.storage.azure.containerName is required when backend is azure")
		}
	case cortexv1alpha1.StorageBackendSwift:
		if cortex.Spec.Storage.Swift == nil || cortex.Spec.Storage.Swift.ContainerName == "" {
			return warnings, fmt.Errorf("spec.storage.swift.containerName is required when backend is swift")
		}
	default:
		return warnings, fmt.Errorf("spec.storage.backend must be one of: s3, gcs, azure, swift")
	}

	// Ingester replicas >= replication factor.
	if cortex.Spec.Ingester != nil && cortex.Spec.Ring != nil {
		rf := cortex.Spec.Ring.GetReplicationFactor()
		replicas := cortex.Spec.Ingester.GetReplicas()
		if replicas < rf {
			return warnings, fmt.Errorf("spec.ingester.replicas (%d) must be >= ring.replicationFactor (%d)", replicas, rf)
		}
	}

	// Zone awareness validation.
	if cortex.Spec.ZoneAwareness != nil && cortex.Spec.ZoneAwareness.Enabled {
		zones := cortex.Spec.ZoneAwareness.Zones

		if len(zones) < 2 {
			return warnings, fmt.Errorf("spec.zoneAwareness.zones must have at least 2 entries when zone awareness is enabled")
		}

		// Check for duplicate zone names.
		seen := make(map[string]bool)
		for _, z := range zones {
			if seen[z] {
				return warnings, fmt.Errorf("spec.zoneAwareness.zones contains duplicate zone %q", z)
			}
			seen[z] = true
		}

		zoneCount := int32(len(zones))

		// Ingester replicas must be divisible by zone count.
		if cortex.Spec.Ingester != nil {
			ingesterReplicas := cortex.Spec.Ingester.GetReplicas()
			if ingesterReplicas%zoneCount != 0 {
				return warnings, fmt.Errorf("spec.ingester.replicas (%d) must be divisible by zone count (%d)", ingesterReplicas, zoneCount)
			}
		}

		// Store-gateway replicas must be divisible by zone count.
		if cortex.Spec.StoreGateway != nil {
			sgReplicas := cortex.Spec.StoreGateway.GetReplicas()
			if sgReplicas%zoneCount != 0 {
				return warnings, fmt.Errorf("spec.storeGateway.replicas (%d) must be divisible by zone count (%d)", sgReplicas, zoneCount)
			}
		}

		// Compactor replicas must be divisible by zone count.
		if cortex.Spec.Compactor != nil {
			compactorReplicas := cortex.Spec.Compactor.GetReplicas()
			if compactorReplicas%zoneCount != 0 {
				return warnings, fmt.Errorf("spec.compactor.replicas (%d) must be divisible by zone count (%d)", compactorReplicas, zoneCount)
			}
		}

		// Warn if zone count < replication factor.
		if cortex.Spec.Ring != nil {
			rf := cortex.Spec.Ring.GetReplicationFactor()
			if zoneCount < rf {
				warnings = append(warnings, fmt.Sprintf("zone count (%d) is less than replication factor (%d) — zone-aware replication may not provide full zone failure tolerance", zoneCount, rf))
			}
		}
	}

	return warnings, nil
}
