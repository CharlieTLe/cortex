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
	"sigs.k8s.io/controller-runtime/pkg/client"
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

	Context("When creating a Cortex resource with zone awareness", func() {
		var cortex *cortexv1alpha1.Cortex

		BeforeEach(func() {
			cortex = &cortexv1alpha1.Cortex{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "zone-test",
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
							Replicas: int32Ptr(6),
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
							Replicas: int32Ptr(3),
						},
						ShardingEnabled: boolPtr(true),
					},
					Compactor: &cortexv1alpha1.CompactorComponentSpec{
						ComponentSpec: cortexv1alpha1.ComponentSpec{
							Replicas: int32Ptr(3),
						},
					},
					ZoneAwareness: &cortexv1alpha1.ZoneAwarenessSpec{
						Enabled:     true,
						Zones:       []string{"zone-a", "zone-b", "zone-c"},
						TopologyKey: "topology.kubernetes.io/zone",
					},
				},
			}

			Expect(k8sClient.Create(ctx, cortex)).Should(Succeed())

			reconciler := &CortexReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      "zone-test",
					Namespace: cortexNamespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())
		})

		AfterEach(func() {
			cr := &cortexv1alpha1.Cortex{}
			err := k8sClient.Get(ctx, types.NamespacedName{Name: "zone-test", Namespace: cortexNamespace}, cr)
			if err == nil {
				Expect(k8sClient.Delete(ctx, cr)).Should(Succeed())
			}
		})

		It("should create per-zone ingester StatefulSets", func() {
			for _, zone := range []string{"zone-a", "zone-b", "zone-c"} {
				sts := &appsv1.StatefulSet{}
				stsName := "zone-test-ingester-zone-" + zone
				Eventually(func() error {
					return k8sClient.Get(ctx, types.NamespacedName{
						Name:      stsName,
						Namespace: cortexNamespace,
					}, sts)
				}, timeout, interval).Should(Succeed(), "StatefulSet for ingester zone %s should exist", zone)

				Expect(*sts.Spec.Replicas).To(Equal(int32(2)), "Each zone should have 2 replicas (6/3)")
				Expect(sts.Labels[LabelZone]).To(Equal(zone))
				Expect(sts.Spec.Template.Spec.Containers[0].Env).To(ContainElement(
					corev1.EnvVar{Name: "CORTEX_AVAILABILITY_ZONE", Value: zone},
				))
			}
		})

		It("should create per-zone store-gateway StatefulSets", func() {
			for _, zone := range []string{"zone-a", "zone-b", "zone-c"} {
				sts := &appsv1.StatefulSet{}
				stsName := "zone-test-store-gateway-zone-" + zone
				Eventually(func() error {
					return k8sClient.Get(ctx, types.NamespacedName{
						Name:      stsName,
						Namespace: cortexNamespace,
					}, sts)
				}, timeout, interval).Should(Succeed(), "StatefulSet for store-gateway zone %s should exist", zone)

				Expect(*sts.Spec.Replicas).To(Equal(int32(1)), "Each zone should have 1 replica (3/3)")
				Expect(sts.Labels[LabelZone]).To(Equal(zone))
			}
		})

		It("should create per-zone headless services", func() {
			for _, component := range []string{ComponentIngester, ComponentStoreGateway, ComponentCompactor} {
				for _, zone := range []string{"zone-a", "zone-b", "zone-c"} {
					svc := &corev1.Service{}
					svcName := "zone-test-" + component + "-zone-" + zone + "-headless"
					Eventually(func() error {
						return k8sClient.Get(ctx, types.NamespacedName{
							Name:      svcName,
							Namespace: cortexNamespace,
						}, svc)
					}, timeout, interval).Should(Succeed(), "Headless service for %s zone %s should exist", component, zone)

					Expect(svc.Spec.ClusterIP).To(Equal("None"))
					Expect(svc.Labels[LabelZone]).To(Equal(zone))
				}
			}
		})

		It("should create per-zone PDBs", func() {
			for _, component := range []string{ComponentIngester, ComponentStoreGateway, ComponentCompactor} {
				for _, zone := range []string{"zone-a", "zone-b", "zone-c"} {
					pdb := &policyv1.PodDisruptionBudget{}
					pdbName := "zone-test-" + component + "-zone-" + zone
					Eventually(func() error {
						return k8sClient.Get(ctx, types.NamespacedName{
							Name:      pdbName,
							Namespace: cortexNamespace,
						}, pdb)
					}, timeout, interval).Should(Succeed(), "PDB for %s zone %s should exist", component, zone)

					Expect(pdb.Spec.MaxUnavailable.IntValue()).To(Equal(1))
					Expect(pdb.Labels[LabelZone]).To(Equal(zone))
				}
			}
		})

		It("should still have cross-zone headless services", func() {
			for _, component := range []string{ComponentIngester, ComponentStoreGateway, ComponentCompactor} {
				svc := &corev1.Service{}
				svcName := "zone-test-" + component + "-headless"
				Eventually(func() error {
					return k8sClient.Get(ctx, types.NamespacedName{
						Name:      svcName,
						Namespace: cortexNamespace,
					}, svc)
				}, timeout, interval).Should(Succeed(), "Cross-zone headless service for %s should exist", component)

				Expect(svc.Spec.ClusterIP).To(Equal("None"))
			}
		})

		It("should not create non-zone StatefulSets for ingester, store-gateway, and compactor", func() {
			for _, component := range []string{ComponentIngester, ComponentStoreGateway, ComponentCompactor} {
				sts := &appsv1.StatefulSet{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      "zone-test-" + component,
					Namespace: cortexNamespace,
				}, sts)
				Expect(err).To(HaveOccurred(), "Non-zone StatefulSet for %s should not exist", component)
			}
		})

		It("should create per-zone compactor StatefulSets", func() {
			for _, zone := range []string{"zone-a", "zone-b", "zone-c"} {
				sts := &appsv1.StatefulSet{}
				stsName := "zone-test-compactor-zone-" + zone
				Eventually(func() error {
					return k8sClient.Get(ctx, types.NamespacedName{
						Name:      stsName,
						Namespace: cortexNamespace,
					}, sts)
				}, timeout, interval).Should(Succeed(), "StatefulSet for compactor zone %s should exist", zone)

				Expect(*sts.Spec.Replicas).To(Equal(int32(1)), "Each zone should have 1 replica (3/3)")
				Expect(sts.Labels[LabelZone]).To(Equal(zone))
			}
		})

		It("should have zone-aware config with availability_zone", func() {
			cm := &corev1.ConfigMap{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      "zone-test-config",
					Namespace: cortexNamespace,
				}, cm)
			}, timeout, interval).Should(Succeed())

			configYAML := cm.Data["cortex.yaml"]
			Expect(configYAML).To(ContainSubstring("availability_zone"))
			Expect(configYAML).To(ContainSubstring("zone_awareness_enabled"))
		})
	})

	Context("When updating a zone-aware Cortex resource", func() {
		var (
			cortex     *cortexv1alpha1.Cortex
			reconciler *CortexReconciler
		)

		BeforeEach(func() {
			cortex = &cortexv1alpha1.Cortex{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "rollout-test",
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
							Replicas: int32Ptr(6),
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
							Replicas: int32Ptr(3),
						},
						ShardingEnabled: boolPtr(true),
					},
					Compactor: &cortexv1alpha1.CompactorComponentSpec{
						ComponentSpec: cortexv1alpha1.ComponentSpec{
							Replicas: int32Ptr(3),
						},
					},
					ZoneAwareness: &cortexv1alpha1.ZoneAwarenessSpec{
						Enabled:     true,
						Zones:       []string{"zone-a", "zone-b", "zone-c"},
						TopologyKey: "topology.kubernetes.io/zone",
					},
				},
			}

			Expect(k8sClient.Create(ctx, cortex)).Should(Succeed())

			reconciler = &CortexReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
		})

		AfterEach(func() {
			// Clean up all resources (envtest doesn't run GC for owner references).
			for _, component := range []string{ComponentIngester, ComponentStoreGateway, ComponentCompactor} {
				for _, zone := range []string{"zone-a", "zone-b", "zone-c"} {
					sts := &appsv1.StatefulSet{}
					if err := k8sClient.Get(ctx, types.NamespacedName{Name: "rollout-test-" + component + "-zone-" + zone, Namespace: cortexNamespace}, sts); err == nil {
						_ = k8sClient.Delete(ctx, sts)
					}
					svc := &corev1.Service{}
					if err := k8sClient.Get(ctx, types.NamespacedName{Name: "rollout-test-" + component + "-zone-" + zone + "-headless", Namespace: cortexNamespace}, svc); err == nil {
						_ = k8sClient.Delete(ctx, svc)
					}
					pdb := &policyv1.PodDisruptionBudget{}
					if err := k8sClient.Get(ctx, types.NamespacedName{Name: "rollout-test-" + component + "-zone-" + zone, Namespace: cortexNamespace}, pdb); err == nil {
						_ = k8sClient.Delete(ctx, pdb)
					}
				}
				svc := &corev1.Service{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: "rollout-test-" + component + "-headless", Namespace: cortexNamespace}, svc); err == nil {
					_ = k8sClient.Delete(ctx, svc)
				}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: "rollout-test-" + component, Namespace: cortexNamespace}, svc); err == nil {
					_ = k8sClient.Delete(ctx, svc)
				}
			}
			for _, component := range []string{ComponentDistributor, ComponentQuerier, ComponentQueryFrontend} {
				dep := &appsv1.Deployment{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: "rollout-test-" + component, Namespace: cortexNamespace}, dep); err == nil {
					_ = k8sClient.Delete(ctx, dep)
				}
				svc := &corev1.Service{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: "rollout-test-" + component, Namespace: cortexNamespace}, svc); err == nil {
					_ = k8sClient.Delete(ctx, svc)
				}
			}
			for _, name := range []string{"rollout-test-query-frontend-headless", "rollout-test-gossip", "rollout-test-config"} {
				obj := &corev1.Service{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: cortexNamespace}, obj); err == nil {
					_ = k8sClient.Delete(ctx, obj)
				}
			}
			cm := &corev1.ConfigMap{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: "rollout-test-config", Namespace: cortexNamespace}, cm); err == nil {
				_ = k8sClient.Delete(ctx, cm)
			}
			cr := &cortexv1alpha1.Cortex{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: "rollout-test", Namespace: cortexNamespace}, cr); err == nil {
				Expect(k8sClient.Delete(ctx, cr)).Should(Succeed())
			}
		})

		It("should create all zone StatefulSets in one pass on initial creation", func() {
			result, err := reconciler.Reconcile(ctx, ctrl.Request{
				NamespacedName: types.NamespacedName{Name: "rollout-test", Namespace: cortexNamespace},
			})
			Expect(err).NotTo(HaveOccurred())
			// No requeue needed on initial creation — all StatefulSets are created in one pass.
			Expect(result.RequeueAfter).To(BeZero())

			for _, zone := range []string{"zone-a", "zone-b", "zone-c"} {
				sts := &appsv1.StatefulSet{}
				Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      "rollout-test-ingester-zone-" + zone,
					Namespace: cortexNamespace,
				}, sts)).To(Succeed(), "Ingester StatefulSet for zone %s should exist", zone)
			}
		})

		It("should update only one zone at a time during rolling restart", func() {
			// Initial reconcile — creates all StatefulSets.
			result, err := reconciler.Reconcile(ctx, ctrl.Request{
				NamespacedName: types.NamespacedName{Name: "rollout-test", Namespace: cortexNamespace},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(BeZero())

			// Simulate all zone StatefulSets being fully rolled out.
			zones := []string{"zone-a", "zone-b", "zone-c"}
			for _, zone := range zones {
				simulateStatefulSetRolledOut(k8sClient, "rollout-test-ingester-zone-"+zone, cortexNamespace, 2)
				simulateStatefulSetRolledOut(k8sClient, "rollout-test-store-gateway-zone-"+zone, cortexNamespace, 1)
				simulateStatefulSetRolledOut(k8sClient, "rollout-test-compactor-zone-"+zone, cortexNamespace, 1)
			}

			// Capture the original config hash from zone-a ingester.
			zoneASts := &appsv1.StatefulSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "rollout-test-ingester-zone-zone-a", Namespace: cortexNamespace}, zoneASts)).To(Succeed())
			oldHash := zoneASts.Spec.Template.Annotations[AnnotationConfigHash]
			Expect(oldHash).NotTo(BeEmpty())

			// Change config by toggling auth.
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "rollout-test", Namespace: cortexNamespace}, cortex)).To(Succeed())
			cortex.Spec.AuthEnabled = boolPtr(false)
			Expect(k8sClient.Update(ctx, cortex)).Should(Succeed())

			// Reconcile 1 — should update zone-a only, requeue.
			result, err = reconciler.Reconcile(ctx, ctrl.Request{
				NamespacedName: types.NamespacedName{Name: "rollout-test", Namespace: cortexNamespace},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(BeNumerically(">", 0), "Should requeue after updating zone-a")

			// Verify zone-a has the new hash, zone-b and zone-c are unchanged.
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "rollout-test-ingester-zone-zone-a", Namespace: cortexNamespace}, zoneASts)).To(Succeed())
			newHash := zoneASts.Spec.Template.Annotations[AnnotationConfigHash]
			Expect(newHash).NotTo(Equal(oldHash), "zone-a should have new config hash")

			zoneBSts := &appsv1.StatefulSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "rollout-test-ingester-zone-zone-b", Namespace: cortexNamespace}, zoneBSts)).To(Succeed())
			Expect(zoneBSts.Spec.Template.Annotations[AnnotationConfigHash]).To(Equal(oldHash), "zone-b should still have old config hash")

			zoneCSts := &appsv1.StatefulSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "rollout-test-ingester-zone-zone-c", Namespace: cortexNamespace}, zoneCSts)).To(Succeed())
			Expect(zoneCSts.Spec.Template.Annotations[AnnotationConfigHash]).To(Equal(oldHash), "zone-c should still have old config hash")

			// Simulate zone-a rolled out.
			simulateStatefulSetRolledOut(k8sClient, "rollout-test-ingester-zone-zone-a", cortexNamespace, 2)
			simulateStatefulSetRolledOut(k8sClient, "rollout-test-store-gateway-zone-zone-a", cortexNamespace, 1)
			simulateStatefulSetRolledOut(k8sClient, "rollout-test-compactor-zone-zone-a", cortexNamespace, 1)

			// Reconcile 2 — should update zone-b, requeue.
			result, err = reconciler.Reconcile(ctx, ctrl.Request{
				NamespacedName: types.NamespacedName{Name: "rollout-test", Namespace: cortexNamespace},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(BeNumerically(">", 0), "Should requeue after updating zone-b")

			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "rollout-test-ingester-zone-zone-b", Namespace: cortexNamespace}, zoneBSts)).To(Succeed())
			Expect(zoneBSts.Spec.Template.Annotations[AnnotationConfigHash]).To(Equal(newHash), "zone-b should now have new config hash")

			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "rollout-test-ingester-zone-zone-c", Namespace: cortexNamespace}, zoneCSts)).To(Succeed())
			Expect(zoneCSts.Spec.Template.Annotations[AnnotationConfigHash]).To(Equal(oldHash), "zone-c should still have old config hash")

			// Simulate zone-b rolled out.
			simulateStatefulSetRolledOut(k8sClient, "rollout-test-ingester-zone-zone-b", cortexNamespace, 2)
			simulateStatefulSetRolledOut(k8sClient, "rollout-test-store-gateway-zone-zone-b", cortexNamespace, 1)
			simulateStatefulSetRolledOut(k8sClient, "rollout-test-compactor-zone-zone-b", cortexNamespace, 1)

			// Reconcile 3 — should update zone-c, requeue.
			result, err = reconciler.Reconcile(ctx, ctrl.Request{
				NamespacedName: types.NamespacedName{Name: "rollout-test", Namespace: cortexNamespace},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(BeNumerically(">", 0), "Should requeue after updating zone-c")

			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "rollout-test-ingester-zone-zone-c", Namespace: cortexNamespace}, zoneCSts)).To(Succeed())
			Expect(zoneCSts.Spec.Template.Annotations[AnnotationConfigHash]).To(Equal(newHash), "zone-c should now have new config hash")

			// Simulate zone-c rolled out.
			simulateStatefulSetRolledOut(k8sClient, "rollout-test-ingester-zone-zone-c", cortexNamespace, 2)
			simulateStatefulSetRolledOut(k8sClient, "rollout-test-store-gateway-zone-zone-c", cortexNamespace, 1)
			simulateStatefulSetRolledOut(k8sClient, "rollout-test-compactor-zone-zone-c", cortexNamespace, 1)

			// Reconcile 4 — all zones rolled out, no requeue.
			result, err = reconciler.Reconcile(ctx, ctrl.Request{
				NamespacedName: types.NamespacedName{Name: "rollout-test", Namespace: cortexNamespace},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(BeZero(), "Should not requeue when all zones are rolled out")
		})
	})
})

// simulateStatefulSetRolledOut updates a StatefulSet's status to simulate a
// completed rollout. This sets ObservedGeneration, ReadyReplicas, UpdatedReplicas,
// and matching revision strings so isStatefulSetRolledOut returns true.
func simulateStatefulSetRolledOut(c client.Client, name, namespace string, replicas int32) {
	sts := &appsv1.StatefulSet{}
	ExpectWithOffset(1, c.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, sts)).To(Succeed())
	sts.Status.ObservedGeneration = sts.Generation
	sts.Status.Replicas = replicas
	sts.Status.ReadyReplicas = replicas
	sts.Status.UpdatedReplicas = replicas
	sts.Status.CurrentRevision = name + "-rev"
	sts.Status.UpdateRevision = name + "-rev"
	ExpectWithOffset(1, c.Status().Update(ctx, sts)).To(Succeed())
}
