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
	"k8s.io/apimachinery/pkg/util/intstr"

	cortexv1alpha1 "github.com/cortexproject/cortex/operator/api/v1alpha1"
	"github.com/cortexproject/cortex/operator/internal/config"
)

// buildComponentService builds a ClusterIP Service for a Cortex component.
func buildComponentService(cortex *cortexv1alpha1.Cortex, component string) *corev1.Service {
	svc := &corev1.Service{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Service",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      resourceName(cortex, component),
			Namespace: cortex.Namespace,
			Labels:    componentLabels(cortex, component),
		},
		Spec: corev1.ServiceSpec{
			Type:     corev1.ServiceTypeClusterIP,
			Selector: selectorLabels(cortex, component),
			Ports: []corev1.ServicePort{
				{
					Name:       PortNameHTTP,
					Port:       int32(config.DefaultHTTPPort),
					TargetPort: intstr.FromString(PortNameHTTP),
					Protocol:   corev1.ProtocolTCP,
				},
				{
					Name:       PortNameGRPC,
					Port:       int32(config.DefaultGRPCPort),
					TargetPort: intstr.FromString(PortNameGRPC),
					Protocol:   corev1.ProtocolTCP,
				},
			},
		},
	}

	// The query-frontend Service needs PublishNotReadyAddresses so that queriers
	// can connect via gRPC before the query-frontend passes readiness checks.
	// Without this, a bootstrap deadlock occurs: query-frontend reports not-ready
	// because no queriers are connected, and queriers can't connect because the
	// Service has no endpoints.
	if component == ComponentQueryFrontend {
		svc.Spec.PublishNotReadyAddresses = true
	}

	setOwnerReference(cortex, svc)
	return svc
}

// buildHeadlessService builds a headless Service for StatefulSet components.
func buildHeadlessService(cortex *cortexv1alpha1.Cortex, component string) *corev1.Service {
	svc := &corev1.Service{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Service",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      headlessServiceName(cortex, component),
			Namespace: cortex.Namespace,
			Labels:    componentLabels(cortex, component),
		},
		Spec: corev1.ServiceSpec{
			Type:      corev1.ServiceTypeClusterIP,
			ClusterIP: "None",
			Selector:  selectorLabels(cortex, component),
			Ports: []corev1.ServicePort{
				{
					Name:       PortNameHTTP,
					Port:       int32(config.DefaultHTTPPort),
					TargetPort: intstr.FromString(PortNameHTTP),
					Protocol:   corev1.ProtocolTCP,
				},
				{
					Name:       PortNameGRPC,
					Port:       int32(config.DefaultGRPCPort),
					TargetPort: intstr.FromString(PortNameGRPC),
					Protocol:   corev1.ProtocolTCP,
				},
			},
		},
	}

	// The query-frontend headless service needs PublishNotReadyAddresses so that
	// queriers can discover frontend pods via DNS before they pass readiness checks.
	// Without this, the bootstrap deadlock persists: frontends are not-ready because
	// no queriers are connected, and queriers can't discover frontends because DNS
	// returns no addresses.
	if component == ComponentQueryFrontend {
		svc.Spec.PublishNotReadyAddresses = true
	}

	setOwnerReference(cortex, svc)
	return svc
}

// buildGossipService builds the gossip headless Service shared by all memberlist-participating components.
func buildGossipService(cortex *cortexv1alpha1.Cortex) *corev1.Service {
	memberlistPort := int32(config.DefaultMemberlistPort)
	if cortex.Spec.Memberlist != nil {
		memberlistPort = cortex.Spec.Memberlist.GetBindPort()
	}

	svc := &corev1.Service{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Service",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      gossipServiceName(cortex),
			Namespace: cortex.Namespace,
			Labels:    commonLabels(cortex),
		},
		Spec: corev1.ServiceSpec{
			Type:                     corev1.ServiceTypeClusterIP,
			ClusterIP:                "None",
			PublishNotReadyAddresses: true,
			Selector:                 gossipSelectorLabels(cortex),
			Ports: []corev1.ServicePort{
				{
					Name:       PortNameMemberlist,
					Port:       memberlistPort,
					TargetPort: intstr.FromString(PortNameMemberlist),
					Protocol:   corev1.ProtocolTCP,
				},
			},
		},
	}
	setOwnerReference(cortex, svc)
	return svc
}
