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

package controller

import (
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	cortexv1alpha1 "github.com/cortexproject/cortex/operator/api/v1alpha1"
)

// buildIngesterStatefulSets builds the Ingester StatefulSet(s).
// When zone awareness is enabled, one StatefulSet per zone is returned with
// replicas evenly divided. Otherwise a single StatefulSet is returned.
func buildIngesterStatefulSets(cortex *cortexv1alpha1.Cortex, configData string) []*appsv1.StatefulSet {
	compSpec := &cortexv1alpha1.ComponentSpec{}
	var terminationGrace int64 = 2400
	var dataVolumeClaimSpec *corev1.PersistentVolumeClaimSpec

	if cortex.Spec.Ingester != nil {
		compSpec = &cortex.Spec.Ingester.ComponentSpec
		terminationGrace = cortex.Spec.Ingester.GetTerminationGracePeriodSeconds()
		dataVolumeClaimSpec = cortex.Spec.Ingester.DataVolumeClaimSpec
	}

	if dataVolumeClaimSpec == nil {
		defaultPVC := cortexv1alpha1.GetDefaultDataVolumeClaimSpec()
		dataVolumeClaimSpec = &defaultPVC
	}

	if cortex.Spec.IsZoneAwarenessEnabled() {
		zones := cortex.Spec.ZoneAwareness.Zones
		totalReplicas := compSpec.GetReplicas()
		perZone := totalReplicas / int32(len(zones))
		var result []*appsv1.StatefulSet
		for _, zone := range zones {
			result = append(result, buildStatefulSet(cortex, ComponentIngester, compSpec, configData, terminationGrace, dataVolumeClaimSpec, zone, perZone))
		}
		return result
	}

	return []*appsv1.StatefulSet{buildStatefulSet(cortex, ComponentIngester, compSpec, configData, terminationGrace, dataVolumeClaimSpec, "", 0)}
}

// buildStoreGatewayStatefulSets builds the Store Gateway StatefulSet(s).
// When zone awareness is enabled, one StatefulSet per zone is returned.
func buildStoreGatewayStatefulSets(cortex *cortexv1alpha1.Cortex, configData string) []*appsv1.StatefulSet {
	compSpec := &cortexv1alpha1.ComponentSpec{}
	var terminationGrace int64 = 120
	var dataVolumeClaimSpec *corev1.PersistentVolumeClaimSpec

	if cortex.Spec.StoreGateway != nil {
		compSpec = &cortex.Spec.StoreGateway.ComponentSpec
		if cortex.Spec.StoreGateway.DataVolumeClaimSpec != nil {
			dataVolumeClaimSpec = cortex.Spec.StoreGateway.DataVolumeClaimSpec
		}
	}

	if dataVolumeClaimSpec == nil {
		defaultPVC := cortexv1alpha1.GetDefaultDataVolumeClaimSpec()
		dataVolumeClaimSpec = &defaultPVC
	}

	if cortex.Spec.IsZoneAwarenessEnabled() {
		zones := cortex.Spec.ZoneAwareness.Zones
		totalReplicas := compSpec.GetReplicas()
		perZone := totalReplicas / int32(len(zones))
		var result []*appsv1.StatefulSet
		for _, zone := range zones {
			result = append(result, buildStatefulSet(cortex, ComponentStoreGateway, compSpec, configData, terminationGrace, dataVolumeClaimSpec, zone, perZone))
		}
		return result
	}

	return []*appsv1.StatefulSet{buildStatefulSet(cortex, ComponentStoreGateway, compSpec, configData, terminationGrace, dataVolumeClaimSpec, "", 0)}
}

// buildCompactorStatefulSets builds the Compactor StatefulSet(s).
// When zone awareness is enabled, one StatefulSet per zone is returned with
// replicas evenly divided. Otherwise a single StatefulSet is returned.
func buildCompactorStatefulSets(cortex *cortexv1alpha1.Cortex, configData string) []*appsv1.StatefulSet {
	compSpec := &cortexv1alpha1.ComponentSpec{}
	var terminationGrace int64 = 120
	var dataVolumeClaimSpec *corev1.PersistentVolumeClaimSpec

	if cortex.Spec.Compactor != nil {
		compSpec = &cortex.Spec.Compactor.ComponentSpec
		if cortex.Spec.Compactor.DataVolumeClaimSpec != nil {
			dataVolumeClaimSpec = cortex.Spec.Compactor.DataVolumeClaimSpec
		}
	}

	if dataVolumeClaimSpec == nil {
		defaultPVC := cortexv1alpha1.GetDefaultDataVolumeClaimSpec()
		dataVolumeClaimSpec = &defaultPVC
	}

	if cortex.Spec.IsZoneAwarenessEnabled() {
		zones := cortex.Spec.ZoneAwareness.Zones
		totalReplicas := compSpec.GetReplicas()
		perZone := totalReplicas / int32(len(zones))
		var result []*appsv1.StatefulSet
		for _, zone := range zones {
			result = append(result, buildStatefulSet(cortex, ComponentCompactor, compSpec, configData, terminationGrace, dataVolumeClaimSpec, zone, perZone))
		}
		return result
	}

	return []*appsv1.StatefulSet{buildStatefulSet(cortex, ComponentCompactor, compSpec, configData, terminationGrace, dataVolumeClaimSpec, "", 0)}
}

// isStatefulSetRolledOut returns true when a StatefulSet has finished rolling out
// all pods to the current revision.
func isStatefulSetRolledOut(sts *appsv1.StatefulSet) bool {
	if sts.Generation != sts.Status.ObservedGeneration {
		return false
	}
	if sts.Spec.Replicas == nil {
		return false
	}
	return sts.Status.ReadyReplicas == *sts.Spec.Replicas &&
		sts.Status.UpdatedReplicas == *sts.Spec.Replicas &&
		sts.Status.CurrentRevision == sts.Status.UpdateRevision
}

// statefulSetNeedsUpdate returns true when the desired StatefulSet differs from
// the existing one in the fields the operator manages. This avoids comparing
// API-server-defaulted fields (probe defaults, volume mode, security contexts)
// which would cause spurious updates on every reconciliation.
func statefulSetNeedsUpdate(existing, desired *appsv1.StatefulSet) bool {
	// Compare replicas.
	existingReplicas := int32(1)
	if existing.Spec.Replicas != nil {
		existingReplicas = *existing.Spec.Replicas
	}
	desiredReplicas := int32(1)
	if desired.Spec.Replicas != nil {
		desiredReplicas = *desired.Spec.Replicas
	}
	if existingReplicas != desiredReplicas {
		return true
	}

	// Compare config hash annotation (captures all config and image changes).
	existingHash := existing.Spec.Template.Annotations[AnnotationConfigHash]
	desiredHash := desired.Spec.Template.Annotations[AnnotationConfigHash]
	if existingHash != desiredHash {
		return true
	}

	// Compare the container image.
	if len(existing.Spec.Template.Spec.Containers) > 0 && len(desired.Spec.Template.Spec.Containers) > 0 {
		if existing.Spec.Template.Spec.Containers[0].Image != desired.Spec.Template.Spec.Containers[0].Image {
			return true
		}
		// Compare container args (captures target and config file changes).
		if !equality.Semantic.DeepEqual(existing.Spec.Template.Spec.Containers[0].Args, desired.Spec.Template.Spec.Containers[0].Args) {
			return true
		}
		// Compare env vars (captures zone assignment changes).
		if !equality.Semantic.DeepEqual(existing.Spec.Template.Spec.Containers[0].Env, desired.Spec.Template.Spec.Containers[0].Env) {
			return true
		}
		// Compare resource requirements.
		if !equality.Semantic.DeepEqual(existing.Spec.Template.Spec.Containers[0].Resources, desired.Spec.Template.Spec.Containers[0].Resources) {
			return true
		}
	}

	// Compare labels on the template.
	if !equality.Semantic.DeepEqual(existing.Spec.Template.Labels, desired.Spec.Template.Labels) {
		return true
	}

	// Compare labels on the StatefulSet itself.
	if !equality.Semantic.DeepEqual(existing.Labels, desired.Labels) {
		return true
	}

	// Compare node selector, affinity, tolerations, and topology spread constraints.
	if !equality.Semantic.DeepEqual(existing.Spec.Template.Spec.NodeSelector, desired.Spec.Template.Spec.NodeSelector) {
		return true
	}
	if !equality.Semantic.DeepEqual(existing.Spec.Template.Spec.Affinity, desired.Spec.Template.Spec.Affinity) {
		return true
	}
	if !equality.Semantic.DeepEqual(existing.Spec.Template.Spec.Tolerations, desired.Spec.Template.Spec.Tolerations) {
		return true
	}
	if !equality.Semantic.DeepEqual(existing.Spec.Template.Spec.TopologySpreadConstraints, desired.Spec.Template.Spec.TopologySpreadConstraints) {
		return true
	}

	return false
}

// buildComponentStatefulSets returns the desired StatefulSets for a stateful component.
func buildComponentStatefulSets(cortex *cortexv1alpha1.Cortex, component, configData string) []*appsv1.StatefulSet {
	switch component {
	case ComponentIngester:
		return buildIngesterStatefulSets(cortex, configData)
	case ComponentStoreGateway:
		return buildStoreGatewayStatefulSets(cortex, configData)
	case ComponentCompactor:
		return buildCompactorStatefulSets(cortex, configData)
	default:
		return nil
	}
}

// buildStatefulSet builds a generic StatefulSet for a stateful Cortex component.
// When zone is non-empty, the StatefulSet uses zone-specific naming, labels, and
// service name. When replicaOverride is > 0, it is used instead of compSpec.GetReplicas().
func buildStatefulSet(
	cortex *cortexv1alpha1.Cortex,
	component string,
	compSpec *cortexv1alpha1.ComponentSpec,
	configData string,
	terminationGracePeriod int64,
	dataVolumeClaimSpec *corev1.PersistentVolumeClaimSpec,
	zone string,
	replicaOverride int32,
) *appsv1.StatefulSet {
	replicas := compSpec.GetReplicas()
	if replicaOverride > 0 {
		replicas = replicaOverride
	}

	name := resourceName(cortex, component)
	labels := componentLabels(cortex, component)
	selector := selectorLabels(cortex, component)
	svcName := headlessServiceName(cortex, component)

	if zone != "" {
		name = zoneResourceName(cortex, component, zone)
		labels = zoneComponentLabels(cortex, component, zone)
		selector = zoneSelectorLabels(cortex, component, zone)
		svcName = zoneHeadlessServiceName(cortex, component, zone)
	}

	podSpec := buildPodSpec(cortex, component, compSpec, zone)
	podSpec.TerminationGracePeriodSeconds = &terminationGracePeriod

	// Add data volume mount to the container.
	podSpec.Containers[0].VolumeMounts = append(podSpec.Containers[0].VolumeMounts, corev1.VolumeMount{
		Name:      VolumeNameData,
		MountPath: "/data",
	})

	sts := &appsv1.StatefulSet{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "apps/v1",
			Kind:       "StatefulSet",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: cortex.Namespace,
			Labels:    labels,
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas:            &replicas,
			ServiceName:         svcName,
			PodManagementPolicy: appsv1.ParallelPodManagement,
			Selector: &metav1.LabelSelector{
				MatchLabels: selector,
			},
			UpdateStrategy: appsv1.StatefulSetUpdateStrategy{
				Type: appsv1.RollingUpdateStatefulSetStrategyType,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
					Annotations: map[string]string{
						AnnotationConfigHash: configHash(configData),
					},
				},
				Spec: podSpec,
			},
			VolumeClaimTemplates: []corev1.PersistentVolumeClaim{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: VolumeNameData,
					},
					Spec: *dataVolumeClaimSpec,
				},
			},
		},
	}
	setOwnerReference(cortex, sts)
	return sts
}

// buildPodDisruptionBudget builds a PDB for a component.
func buildPodDisruptionBudget(cortex *cortexv1alpha1.Cortex, component string) *policyv1.PodDisruptionBudget {
	maxUnavailable := intstr.FromInt32(1)
	pdb := &policyv1.PodDisruptionBudget{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "policy/v1",
			Kind:       "PodDisruptionBudget",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      resourceName(cortex, component),
			Namespace: cortex.Namespace,
			Labels:    componentLabels(cortex, component),
		},
		Spec: policyv1.PodDisruptionBudgetSpec{
			MaxUnavailable: &maxUnavailable,
			Selector: &metav1.LabelSelector{
				MatchLabels: selectorLabels(cortex, component),
			},
		},
	}
	setOwnerReference(cortex, pdb)
	return pdb
}

// buildZonePodDisruptionBudget builds a PDB for a zone-specific component.
func buildZonePodDisruptionBudget(cortex *cortexv1alpha1.Cortex, component, zone string) *policyv1.PodDisruptionBudget {
	maxUnavailable := intstr.FromInt32(1)
	pdb := &policyv1.PodDisruptionBudget{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "policy/v1",
			Kind:       "PodDisruptionBudget",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      zoneResourceName(cortex, component, zone),
			Namespace: cortex.Namespace,
			Labels:    zoneComponentLabels(cortex, component, zone),
		},
		Spec: policyv1.PodDisruptionBudgetSpec{
			MaxUnavailable: &maxUnavailable,
			Selector: &metav1.LabelSelector{
				MatchLabels: zoneSelectorLabels(cortex, component, zone),
			},
		},
	}
	setOwnerReference(cortex, pdb)
	return pdb
}
