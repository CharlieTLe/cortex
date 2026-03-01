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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	cortexv1alpha1 "github.com/cortexproject/cortex/operator/api/v1alpha1"
)

var _ = Describe("Cortex Controller", func() {
	const (
		timeout  = time.Second * 30
		interval = time.Millisecond * 250

		cortexName      = "test-cortex"
		cortexNamespace = "default"
	)

	Context("When creating a Cortex resource", func() {
		var cortex *cortexv1alpha1.Cortex

		BeforeEach(func() {
			cortex = &cortexv1alpha1.Cortex{
				ObjectMeta: metav1.ObjectMeta{
					Name:      cortexName,
					Namespace: cortexNamespace,
				},
				Spec: cortexv1alpha1.CortexSpec{
					Image: cortexv1alpha1.ImageSpec{
						Repository: "quay.io/cortexproject/cortex",
						Tag:        "v1.17.0",
						PullPolicy: corev1.PullIfNotPresent,
					},
					AuthEnabled: boolPtr(true),
					Storage: cortexv1alpha1.StorageSpec{
						Backend: cortexv1alpha1.StorageBackendS3,
						S3: &cortexv1alpha1.S3StorageSpec{
							BucketName: "cortex-blocks",
							Endpoint:   "minio:9000",
							Insecure:   true,
						},
					},
					Ring: &cortexv1alpha1.RingSpec{
						ReplicationFactor: int32Ptr(3),
						NumTokens:         int32Ptr(512),
					},
					Distributor: &cortexv1alpha1.ComponentSpec{
						Replicas: int32Ptr(2),
					},
					Ingester: &cortexv1alpha1.IngesterComponentSpec{
						ComponentSpec: cortexv1alpha1.ComponentSpec{
							Replicas: int32Ptr(3),
						},
						TerminationGracePeriodSeconds: int64Ptr(2400),
					},
					Querier: &cortexv1alpha1.ComponentSpec{
						Replicas: int32Ptr(2),
					},
					QueryFrontend: &cortexv1alpha1.ComponentSpec{
						Replicas: int32Ptr(1),
					},
					StoreGateway: &cortexv1alpha1.StoreGatewaySpec{
						ComponentSpec: cortexv1alpha1.ComponentSpec{
							Replicas: int32Ptr(2),
						},
						ShardingEnabled: boolPtr(true),
					},
					Compactor: &cortexv1alpha1.CompactorComponentSpec{
						ComponentSpec: cortexv1alpha1.ComponentSpec{
							Replicas: int32Ptr(1),
						},
					},
				},
			}

			Expect(k8sClient.Create(ctx, cortex)).Should(Succeed())

			// Set up the reconciler and run reconciliation.
			reconciler := &CortexReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      cortexName,
					Namespace: cortexNamespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())
		})

		AfterEach(func() {
			// Clean up the CR.
			cr := &cortexv1alpha1.Cortex{}
			err := k8sClient.Get(ctx, types.NamespacedName{Name: cortexName, Namespace: cortexNamespace}, cr)
			if err == nil {
				Expect(k8sClient.Delete(ctx, cr)).Should(Succeed())
			}
		})

		It("should create the ConfigMap", func() {
			cm := &corev1.ConfigMap{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      cortexName + "-config",
					Namespace: cortexNamespace,
				}, cm)
			}, timeout, interval).Should(Succeed())

			Expect(cm.Data).To(HaveKey("cortex.yaml"))
			Expect(cm.Data["cortex.yaml"]).To(ContainSubstring("auth_enabled: true"))
			Expect(cm.Data["cortex.yaml"]).To(ContainSubstring("backend: s3"))
		})

		It("should create the gossip headless service", func() {
			svc := &corev1.Service{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      cortexName + "-gossip",
					Namespace: cortexNamespace,
				}, svc)
			}, timeout, interval).Should(Succeed())

			Expect(svc.Spec.ClusterIP).To(Equal("None"))
			Expect(svc.Spec.Ports).To(HaveLen(1))
			Expect(svc.Spec.Ports[0].Name).To(Equal("memberlist"))
		})

		It("should create Deployments for stateless components", func() {
			components := map[string]int32{
				ComponentDistributor:   2,
				ComponentQuerier:       2,
				ComponentQueryFrontend: 1,
			}

			for component, expectedReplicas := range components {
				dep := &appsv1.Deployment{}
				Eventually(func() error {
					return k8sClient.Get(ctx, types.NamespacedName{
						Name:      cortexName + "-" + component,
						Namespace: cortexNamespace,
					}, dep)
				}, timeout, interval).Should(Succeed(), "Deployment for %s should exist", component)

				Expect(*dep.Spec.Replicas).To(Equal(expectedReplicas), "Replicas for %s", component)
				Expect(dep.Spec.Template.Annotations).To(HaveKey(AnnotationConfigHash))
				Expect(dep.Spec.Template.Spec.Containers).To(HaveLen(1))
				Expect(dep.Spec.Template.Spec.Containers[0].Image).To(Equal("quay.io/cortexproject/cortex:v1.17.0"))
			}
		})

		It("should create StatefulSets for stateful components", func() {
			// Ingester
			ingesterSts := &appsv1.StatefulSet{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      cortexName + "-ingester",
					Namespace: cortexNamespace,
				}, ingesterSts)
			}, timeout, interval).Should(Succeed())

			Expect(*ingesterSts.Spec.Replicas).To(Equal(int32(3)))
			Expect(ingesterSts.Spec.VolumeClaimTemplates).To(HaveLen(1))
			Expect(*ingesterSts.Spec.Template.Spec.TerminationGracePeriodSeconds).To(Equal(int64(2400)))

			// Store Gateway
			sgSts := &appsv1.StatefulSet{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      cortexName + "-store-gateway",
					Namespace: cortexNamespace,
				}, sgSts)
			}, timeout, interval).Should(Succeed())

			Expect(*sgSts.Spec.Replicas).To(Equal(int32(2)))

			// Compactor
			compactorSts := &appsv1.StatefulSet{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      cortexName + "-compactor",
					Namespace: cortexNamespace,
				}, compactorSts)
			}, timeout, interval).Should(Succeed())

			Expect(*compactorSts.Spec.Replicas).To(Equal(int32(1)))
			Expect(compactorSts.Spec.VolumeClaimTemplates).To(HaveLen(1))
		})

		It("should create PodDisruptionBudgets for stateful components", func() {
			for _, component := range []string{ComponentIngester, ComponentStoreGateway, ComponentCompactor} {
				pdb := &policyv1.PodDisruptionBudget{}
				Eventually(func() error {
					return k8sClient.Get(ctx, types.NamespacedName{
						Name:      cortexName + "-" + component,
						Namespace: cortexNamespace,
					}, pdb)
				}, timeout, interval).Should(Succeed(), "PDB for %s should exist", component)

				Expect(pdb.Spec.MaxUnavailable.IntValue()).To(Equal(1))
			}
		})

		It("should create headless services for stateful and query-frontend components", func() {
			for _, component := range []string{ComponentIngester, ComponentStoreGateway, ComponentCompactor, ComponentQueryFrontend} {
				svc := &corev1.Service{}
				Eventually(func() error {
					return k8sClient.Get(ctx, types.NamespacedName{
						Name:      cortexName + "-" + component + "-headless",
						Namespace: cortexNamespace,
					}, svc)
				}, timeout, interval).Should(Succeed(), "Headless service for %s should exist", component)

				Expect(svc.Spec.ClusterIP).To(Equal("None"))
			}
		})

		It("should create component services", func() {
			for _, component := range []string{ComponentDistributor, ComponentQuerier, ComponentQueryFrontend, ComponentCompactor} {
				svc := &corev1.Service{}
				Eventually(func() error {
					return k8sClient.Get(ctx, types.NamespacedName{
						Name:      cortexName + "-" + component,
						Namespace: cortexNamespace,
					}, svc)
				}, timeout, interval).Should(Succeed(), "Service for %s should exist", component)

				Expect(svc.Spec.Ports).To(HaveLen(2))
			}
		})
	})

	Context("When updating a Cortex resource", func() {
		It("should update deployment replicas on spec change", func() {
			cortex := &cortexv1alpha1.Cortex{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "update-test",
					Namespace: cortexNamespace,
				},
				Spec: cortexv1alpha1.CortexSpec{
					Image: cortexv1alpha1.ImageSpec{
						Repository: "quay.io/cortexproject/cortex",
						Tag:        "v1.17.0",
						PullPolicy: corev1.PullIfNotPresent,
					},
					Storage: cortexv1alpha1.StorageSpec{
						Backend: cortexv1alpha1.StorageBackendS3,
						S3:      &cortexv1alpha1.S3StorageSpec{BucketName: "test"},
					},
					Distributor: &cortexv1alpha1.ComponentSpec{Replicas: int32Ptr(1)},
					Ingester: &cortexv1alpha1.IngesterComponentSpec{
						ComponentSpec: cortexv1alpha1.ComponentSpec{Replicas: int32Ptr(3)},
					},
				},
			}
			Expect(k8sClient.Create(ctx, cortex)).Should(Succeed())

			reconciler := &CortexReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, ctrl.Request{
				NamespacedName: types.NamespacedName{Name: "update-test", Namespace: cortexNamespace},
			})
			Expect(err).NotTo(HaveOccurred())

			// Verify initial replicas.
			dep := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "update-test-distributor", Namespace: cortexNamespace}, dep)).To(Succeed())
			Expect(*dep.Spec.Replicas).To(Equal(int32(1)))

			// Update replicas.
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "update-test", Namespace: cortexNamespace}, cortex)).To(Succeed())
			cortex.Spec.Distributor.Replicas = int32Ptr(3)
			Expect(k8sClient.Update(ctx, cortex)).Should(Succeed())

			_, err = reconciler.Reconcile(ctx, ctrl.Request{
				NamespacedName: types.NamespacedName{Name: "update-test", Namespace: cortexNamespace},
			})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "update-test-distributor", Namespace: cortexNamespace}, dep)).To(Succeed())
			Expect(*dep.Spec.Replicas).To(Equal(int32(3)))

			// Clean up.
			Expect(k8sClient.Delete(ctx, cortex)).Should(Succeed())
		})

		It("should update config hash annotation on config change", func() {
			cortex := &cortexv1alpha1.Cortex{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "hash-test",
					Namespace: cortexNamespace,
				},
				Spec: cortexv1alpha1.CortexSpec{
					Image: cortexv1alpha1.ImageSpec{
						Repository: "quay.io/cortexproject/cortex",
						Tag:        "v1.17.0",
						PullPolicy: corev1.PullIfNotPresent,
					},
					Storage: cortexv1alpha1.StorageSpec{
						Backend: cortexv1alpha1.StorageBackendS3,
						S3:      &cortexv1alpha1.S3StorageSpec{BucketName: "test"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, cortex)).Should(Succeed())

			reconciler := &CortexReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, ctrl.Request{
				NamespacedName: types.NamespacedName{Name: "hash-test", Namespace: cortexNamespace},
			})
			Expect(err).NotTo(HaveOccurred())

			dep := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "hash-test-distributor", Namespace: cortexNamespace}, dep)).To(Succeed())
			oldHash := dep.Spec.Template.Annotations[AnnotationConfigHash]
			Expect(oldHash).NotTo(BeEmpty())

			// Change config by disabling auth.
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "hash-test", Namespace: cortexNamespace}, cortex)).To(Succeed())
			cortex.Spec.AuthEnabled = boolPtr(false)
			Expect(k8sClient.Update(ctx, cortex)).Should(Succeed())

			_, err = reconciler.Reconcile(ctx, ctrl.Request{
				NamespacedName: types.NamespacedName{Name: "hash-test", Namespace: cortexNamespace},
			})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "hash-test-distributor", Namespace: cortexNamespace}, dep)).To(Succeed())
			newHash := dep.Spec.Template.Annotations[AnnotationConfigHash]
			Expect(newHash).NotTo(Equal(oldHash), "config hash should change when auth_enabled changes")

			// Clean up.
			Expect(k8sClient.Delete(ctx, cortex)).Should(Succeed())
		})
	})
})
