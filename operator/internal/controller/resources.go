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
	"crypto/sha256"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	cortexv1alpha1 "github.com/cortexproject/cortex/operator/api/v1alpha1"
	"github.com/cortexproject/cortex/operator/internal/config"
)

const (
	// Component names (used as target flags).
	ComponentDistributor   = "distributor"
	ComponentIngester      = "ingester"
	ComponentQuerier       = "querier"
	ComponentQueryFrontend = "query-frontend"
	ComponentStoreGateway  = "store-gateway"
	ComponentCompactor     = "compactor"

	// Label keys.
	LabelName      = "app.kubernetes.io/name"
	LabelInstance  = "app.kubernetes.io/instance"
	LabelComponent = "app.kubernetes.io/component"
	LabelManagedBy = "app.kubernetes.io/managed-by"
	LabelPartOf    = "app.kubernetes.io/part-of"

	// Annotation keys.
	AnnotationConfigHash = "cortex.io/config-hash"

	// Port names.
	PortNameHTTP       = "http"
	PortNameGRPC       = "grpc"
	PortNameMemberlist = "memberlist"

	// Volume names.
	VolumeNameConfig        = "config"
	VolumeNameData          = "data"
	VolumeNameRuntimeConfig = "runtime-config"
)

// commonLabels returns the base set of labels for all resources.
func commonLabels(cortex *cortexv1alpha1.Cortex) map[string]string {
	return map[string]string{
		LabelName:      "cortex",
		LabelInstance:  cortex.Name,
		LabelManagedBy: "cortex-operator",
		LabelPartOf:    cortex.Name,
	}
}

// componentLabels returns labels for a specific component.
func componentLabels(cortex *cortexv1alpha1.Cortex, component string) map[string]string {
	labels := commonLabels(cortex)
	labels[LabelComponent] = component
	return labels
}

// selectorLabels returns the minimal labels used in label selectors.
func selectorLabels(cortex *cortexv1alpha1.Cortex, component string) map[string]string {
	return map[string]string{
		LabelInstance:  cortex.Name,
		LabelComponent: component,
	}
}

// gossipSelectorLabels returns labels that select all memberlist-participating pods.
func gossipSelectorLabels(cortex *cortexv1alpha1.Cortex) map[string]string {
	return map[string]string{
		LabelPartOf: cortex.Name,
	}
}

// configHash computes a SHA-256 hash of the config data for pod annotations.
func configHash(configData string) string {
	h := sha256.Sum256([]byte(configData))
	return fmt.Sprintf("%x", h)
}

// resourceName returns the name for a component resource.
func resourceName(cortex *cortexv1alpha1.Cortex, component string) string {
	return fmt.Sprintf("%s-%s", cortex.Name, component)
}

// configMapName returns the name of the shared ConfigMap.
func configMapName(cortex *cortexv1alpha1.Cortex) string {
	return fmt.Sprintf("%s-config", cortex.Name)
}

// gossipServiceName returns the name of the gossip headless service.
func gossipServiceName(cortex *cortexv1alpha1.Cortex) string {
	return fmt.Sprintf("%s-gossip", cortex.Name)
}

// headlessServiceName returns the name for a component's headless service (StatefulSets).
func headlessServiceName(cortex *cortexv1alpha1.Cortex, component string) string {
	return fmt.Sprintf("%s-%s-headless", cortex.Name, component)
}

// setOwnerReference sets the owner reference on a resource.
func setOwnerReference(cortex *cortexv1alpha1.Cortex, obj metav1.Object) {
	obj.SetOwnerReferences([]metav1.OwnerReference{
		{
			APIVersion: cortex.APIVersion,
			Kind:       cortex.Kind,
			Name:       cortex.Name,
			UID:        cortex.UID,
			Controller: boolPtr(true),
		},
	})
}

// containerForComponent builds the main container spec for a Cortex component.
func containerForComponent(cortex *cortexv1alpha1.Cortex, component string, compSpec *cortexv1alpha1.ComponentSpec) corev1.Container {
	args := []string{
		fmt.Sprintf("-target=%s", component),
		fmt.Sprintf("-config.file=%s/%s", config.DefaultConfigDir, config.DefaultConfigFile),
		"-config.expand-env=true",
	}

	if compSpec != nil && len(compSpec.ExtraArgs) > 0 {
		args = append(args, compSpec.ExtraArgs...)
	}

	ports := []corev1.ContainerPort{
		{Name: PortNameHTTP, ContainerPort: int32(config.DefaultHTTPPort), Protocol: corev1.ProtocolTCP},
		{Name: PortNameGRPC, ContainerPort: int32(config.DefaultGRPCPort), Protocol: corev1.ProtocolTCP},
	}

	// All components except query-frontend participate in memberlist gossip.
	if component != ComponentQueryFrontend {
		memberlistPort := int32(config.DefaultMemberlistPort)
		if cortex.Spec.Memberlist != nil {
			memberlistPort = cortex.Spec.Memberlist.GetBindPort()
		}
		ports = append(ports, corev1.ContainerPort{
			Name: PortNameMemberlist, ContainerPort: memberlistPort, Protocol: corev1.ProtocolTCP,
		})
	}

	volumeMounts := []corev1.VolumeMount{
		{Name: VolumeNameConfig, MountPath: config.DefaultConfigDir},
	}

	if cortex.Spec.RuntimeConfig != nil {
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name:      VolumeNameRuntimeConfig,
			MountPath: config.DefaultRuntimeConfigDir,
		})
	}

	readyProbe := corev1.ProbeHandler{
		HTTPGet: &corev1.HTTPGetAction{
			Path: "/ready",
			Port: intstr.FromString(PortNameHTTP),
		},
	}

	container := corev1.Container{
		Name:  "cortex",
		Image: fmt.Sprintf("%s:%s", cortex.Spec.Image.Repository, cortex.Spec.Image.Tag),
		Args:  args,
		Ports: ports,
		// StartupProbe gives components time to initialize (ring stabilization,
		// TSDB sync, etc.) before liveness checks begin. Allows up to 5 minutes.
		StartupProbe: &corev1.Probe{
			ProbeHandler:     readyProbe,
			PeriodSeconds:    10,
			FailureThreshold: 30,
		},
		ReadinessProbe: &corev1.Probe{
			ProbeHandler:  readyProbe,
			PeriodSeconds: 10,
		},
		LivenessProbe: &corev1.Probe{
			ProbeHandler:  readyProbe,
			PeriodSeconds: 30,
		},
		VolumeMounts:    volumeMounts,
		ImagePullPolicy: cortex.Spec.Image.PullPolicy,
	}

	if compSpec != nil {
		if compSpec.Resources != nil {
			container.Resources = *compSpec.Resources
		}
		if len(compSpec.ExtraEnv) > 0 {
			container.Env = append(container.Env, compSpec.ExtraEnv...)
		}
	}

	// Add env vars from storage secret if configured.
	if cortex.Spec.Storage.SecretRef != nil {
		container.EnvFrom = append(container.EnvFrom, corev1.EnvFromSource{
			SecretRef: &corev1.SecretEnvSource{
				LocalObjectReference: *cortex.Spec.Storage.SecretRef,
			},
		})
	}

	return container
}

// configVolume returns the config volume spec.
func configVolume(cortex *cortexv1alpha1.Cortex) corev1.Volume {
	return corev1.Volume{
		Name: VolumeNameConfig,
		VolumeSource: corev1.VolumeSource{
			ConfigMap: &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{
					Name: configMapName(cortex),
				},
			},
		},
	}
}

// runtimeConfigVolume returns the runtime config volume spec.
func runtimeConfigVolume(cortex *cortexv1alpha1.Cortex) corev1.Volume {
	return corev1.Volume{
		Name: VolumeNameRuntimeConfig,
		VolumeSource: corev1.VolumeSource{
			ConfigMap: &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{
					Name: cortex.Spec.RuntimeConfig.Name,
				},
			},
		},
	}
}

// podVolumes returns the volumes for a pod template.
func podVolumes(cortex *cortexv1alpha1.Cortex) []corev1.Volume {
	volumes := []corev1.Volume{configVolume(cortex)}
	if cortex.Spec.RuntimeConfig != nil {
		volumes = append(volumes, runtimeConfigVolume(cortex))
	}
	return volumes
}

func boolPtr(b bool) *bool {
	return &b
}

func int32Ptr(i int32) *int32 {
	return &i
}

func int64Ptr(i int64) *int64 {
	return &i
}
