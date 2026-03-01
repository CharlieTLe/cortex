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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	cortexv1alpha1 "github.com/cortexproject/cortex/operator/api/v1alpha1"
)

// buildIngesterStatefulSet builds the Ingester StatefulSet.
func buildIngesterStatefulSet(cortex *cortexv1alpha1.Cortex, configData string) *appsv1.StatefulSet {
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

	return buildStatefulSet(cortex, ComponentIngester, compSpec, configData, terminationGrace, dataVolumeClaimSpec)
}

// buildStoreGatewayStatefulSet builds the Store Gateway StatefulSet.
func buildStoreGatewayStatefulSet(cortex *cortexv1alpha1.Cortex, configData string) *appsv1.StatefulSet {
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

	return buildStatefulSet(cortex, ComponentStoreGateway, compSpec, configData, terminationGrace, dataVolumeClaimSpec)
}

// buildCompactorStatefulSet builds the Compactor StatefulSet.
func buildCompactorStatefulSet(cortex *cortexv1alpha1.Cortex, configData string) *appsv1.StatefulSet {
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

	return buildStatefulSet(cortex, ComponentCompactor, compSpec, configData, terminationGrace, dataVolumeClaimSpec)
}

// buildStatefulSet builds a generic StatefulSet for a stateful Cortex component.
func buildStatefulSet(
	cortex *cortexv1alpha1.Cortex,
	component string,
	compSpec *cortexv1alpha1.ComponentSpec,
	configData string,
	terminationGracePeriod int64,
	dataVolumeClaimSpec *corev1.PersistentVolumeClaimSpec,
) *appsv1.StatefulSet {
	replicas := compSpec.GetReplicas()
	labels := componentLabels(cortex, component)
	selector := selectorLabels(cortex, component)

	podSpec := buildPodSpec(cortex, component, compSpec)
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
			Name:      resourceName(cortex, component),
			Namespace: cortex.Namespace,
			Labels:    labels,
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas:            &replicas,
			ServiceName:         headlessServiceName(cortex, component),
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
