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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	cortexv1alpha1 "github.com/cortexproject/cortex/operator/api/v1alpha1"
)

// buildConfigMap builds the shared Cortex ConfigMap.
func buildConfigMap(cortex *cortexv1alpha1.Cortex, configData string) *corev1.ConfigMap {
	cm := &corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "ConfigMap",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      configMapName(cortex),
			Namespace: cortex.Namespace,
			Labels:    commonLabels(cortex),
		},
		Data: map[string]string{
			"cortex.yaml": configData,
		},
	}
	setOwnerReference(cortex, cm)
	return cm
}
