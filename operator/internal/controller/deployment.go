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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	cortexv1alpha1 "github.com/cortexproject/cortex/operator/api/v1alpha1"
)

// buildDeployment builds a Deployment for a stateless Cortex component.
func buildDeployment(cortex *cortexv1alpha1.Cortex, component string, compSpec *cortexv1alpha1.ComponentSpec, configData string) *appsv1.Deployment {
	replicas := int32(1)
	if compSpec != nil {
		replicas = compSpec.GetReplicas()
	}

	labels := componentLabels(cortex, component)
	selector := selectorLabels(cortex, component)

	maxSurge := intstr.FromInt32(1)
	maxUnavailable := intstr.FromInt32(0)

	// The query-frontend has a circular readiness dependency with queriers:
	// each querier maintains a single gRPC connection to one frontend, and the
	// frontend's /ready endpoint requires at least one connected querier.
	// With maxSurge=1/maxUnavailable=0, a rolling update creates an extra pod
	// that may never receive a querier connection and thus never become Ready,
	// blocking the rollout. Using maxUnavailable=1 lets the old pod terminate
	// first, freeing the querier connection for the replacement pod.
	if component == ComponentQueryFrontend {
		maxSurge = intstr.FromInt32(0)
		maxUnavailable = intstr.FromInt32(1)
	}

	dep := &appsv1.Deployment{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "apps/v1",
			Kind:       "Deployment",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      resourceName(cortex, component),
			Namespace: cortex.Namespace,
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: selector,
			},
			Strategy: appsv1.DeploymentStrategy{
				Type: appsv1.RollingUpdateDeploymentStrategyType,
				RollingUpdate: &appsv1.RollingUpdateDeployment{
					MaxSurge:       &maxSurge,
					MaxUnavailable: &maxUnavailable,
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
					Annotations: map[string]string{
						AnnotationConfigHash: configHash(configData),
					},
				},
				Spec: buildPodSpec(cortex, component, compSpec),
			},
		},
	}
	setOwnerReference(cortex, dep)
	return dep
}

// buildPodSpec builds the PodSpec for a component.
func buildPodSpec(cortex *cortexv1alpha1.Cortex, component string, compSpec *cortexv1alpha1.ComponentSpec) corev1.PodSpec {
	spec := corev1.PodSpec{
		Containers: []corev1.Container{
			containerForComponent(cortex, component, compSpec),
		},
		Volumes: podVolumes(cortex),
	}

	if len(cortex.Spec.Image.PullSecrets) > 0 {
		spec.ImagePullSecrets = cortex.Spec.Image.PullSecrets
	}

	if compSpec != nil {
		if compSpec.NodeSelector != nil {
			spec.NodeSelector = compSpec.NodeSelector
		}
		if len(compSpec.Tolerations) > 0 {
			spec.Tolerations = compSpec.Tolerations
		}
		if compSpec.Affinity != nil {
			spec.Affinity = compSpec.Affinity
		}
	}

	return spec
}
