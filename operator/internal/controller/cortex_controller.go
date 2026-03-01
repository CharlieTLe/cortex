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
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	cortexv1alpha1 "github.com/cortexproject/cortex/operator/api/v1alpha1"
	"github.com/cortexproject/cortex/operator/internal/config"
)

const (
	conditionTypeReady       = "Ready"
	conditionTypeDegraded    = "Degraded"
	conditionTypeConfigReady = "ConfigReady"
)

// CortexReconciler reconciles a Cortex object
type CortexReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=cortex.cortex.io,resources=cortexes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cortex.cortex.io,resources=cortexes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=cortex.cortex.io,resources=cortexes/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=policy,resources=poddisruptionbudgets,verbs=get;list;watch;create;update;patch;delete

// Reconcile handles reconciliation for Cortex resources.
func (r *CortexReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Fetch the Cortex CR.
	cortex := &cortexv1alpha1.Cortex{}
	if err := r.Get(ctx, req.NamespacedName, cortex); err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("Cortex resource not found, likely deleted")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("fetching Cortex: %w", err)
	}

	// Set API version and Kind (needed for owner references).
	cortex.APIVersion = "cortex.cortex.io/v1alpha1"
	cortex.Kind = "Cortex"

	// Generate or use external config.
	configData, err := r.getConfigData(ctx, cortex)
	if err != nil {
		r.setCondition(cortex, conditionTypeConfigReady, metav1.ConditionFalse, "ConfigError", err.Error())
		if statusErr := r.Status().Update(ctx, cortex); statusErr != nil {
			logger.Error(statusErr, "failed to update status")
		}
		return ctrl.Result{}, fmt.Errorf("generating config: %w", err)
	}
	r.setCondition(cortex, conditionTypeConfigReady, metav1.ConditionTrue, "ConfigGenerated", "Configuration generated successfully")

	// Reconcile all resources in order.
	// 1. ConfigMap
	if err := r.reconcileConfigMap(ctx, cortex, configData); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconciling ConfigMap: %w", err)
	}

	// 2. Gossip headless Service
	if err := r.reconcileService(ctx, buildGossipService(cortex)); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconciling gossip service: %w", err)
	}

	// 3. Ingester (zone-aware or single StatefulSet)
	var requeueNeeded bool
	requeue, err := r.reconcileStatefulComponent(ctx, cortex, ComponentIngester, configData)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("reconciling ingester: %w", err)
	}
	requeueNeeded = requeueNeeded || requeue

	// 4. Store Gateway (zone-aware or single StatefulSet)
	requeue, err = r.reconcileStatefulComponent(ctx, cortex, ComponentStoreGateway, configData)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("reconciling store-gateway: %w", err)
	}
	requeueNeeded = requeueNeeded || requeue

	// 5. Distributor (Service + Deployment)
	if err := r.reconcileService(ctx, buildComponentService(cortex, ComponentDistributor)); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconciling distributor service: %w", err)
	}
	distributorSpec := cortex.Spec.Distributor
	if err := r.reconcileDeployment(ctx, buildDeployment(cortex, ComponentDistributor, distributorSpec, configData)); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconciling distributor deployment: %w", err)
	}

	// 6. Compactor (ClusterIP service + zone-aware or single StatefulSet)
	if err := r.reconcileService(ctx, buildComponentService(cortex, ComponentCompactor)); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconciling compactor service: %w", err)
	}
	requeue, err = r.reconcileStatefulComponent(ctx, cortex, ComponentCompactor, configData)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("reconciling compactor: %w", err)
	}
	requeueNeeded = requeueNeeded || requeue

	// 7. Querier (Service + Deployment)
	if err := r.reconcileService(ctx, buildComponentService(cortex, ComponentQuerier)); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconciling querier service: %w", err)
	}
	querierSpec := cortex.Spec.Querier
	if err := r.reconcileDeployment(ctx, buildDeployment(cortex, ComponentQuerier, querierSpec, configData)); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconciling querier deployment: %w", err)
	}

	// 8. Query Frontend (headless Service + ClusterIP Service + Deployment)
	if err := r.reconcileService(ctx, buildHeadlessService(cortex, ComponentQueryFrontend)); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconciling query-frontend headless service: %w", err)
	}
	if err := r.reconcileService(ctx, buildComponentService(cortex, ComponentQueryFrontend)); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconciling query-frontend service: %w", err)
	}
	qfSpec := cortex.Spec.QueryFrontend
	if err := r.reconcileDeployment(ctx, buildDeployment(cortex, ComponentQueryFrontend, qfSpec, configData)); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconciling query-frontend deployment: %w", err)
	}

	// 9. Update Status
	if err := r.updateStatus(ctx, cortex); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating status: %w", err)
	}

	if requeueNeeded {
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}
	return ctrl.Result{}, nil
}

// reconcileStatefulComponent reconciles a stateful component (ingester or store-gateway)
// with zone awareness support. It handles creation/update of zone-specific or single
// StatefulSets, headless services, and PDBs, plus cleanup of stale resources when
// switching between zone-aware and non-zone modes.
// The returned bool signals whether the reconciler should requeue (zone rollout in progress).
func (r *CortexReconciler) reconcileStatefulComponent(ctx context.Context, cortex *cortexv1alpha1.Cortex, component, configData string) (bool, error) {
	if cortex.Spec.IsZoneAwarenessEnabled() {
		return r.reconcileZoneAwareStatefulComponent(ctx, cortex, component, configData)
	}
	return r.reconcileNonZoneStatefulComponent(ctx, cortex, component, configData)
}

// reconcileZoneAwareStatefulComponent reconciles per-zone StatefulSets, services, and PDBs.
// It uses two-phase logic: first create any missing StatefulSets (all zones simultaneously),
// then apply updates sequentially — one zone at a time — waiting for each zone to finish
// rolling out before updating the next. Returns true if a requeue is needed.
func (r *CortexReconciler) reconcileZoneAwareStatefulComponent(ctx context.Context, cortex *cortexv1alpha1.Cortex, component, configData string) (bool, error) {
	logger := log.FromContext(ctx)

	// Cross-zone headless service (selects all pods regardless of zone).
	if err := r.reconcileService(ctx, buildHeadlessService(cortex, component)); err != nil {
		return false, fmt.Errorf("reconciling %s cross-zone headless service: %w", component, err)
	}

	for _, zone := range cortex.Spec.ZoneAwareness.Zones {
		// Per-zone headless service.
		if err := r.reconcileService(ctx, buildZoneHeadlessService(cortex, component, zone)); err != nil {
			return false, fmt.Errorf("reconciling %s zone %s headless service: %w", component, zone, err)
		}

		// Per-zone PDB.
		if err := r.reconcilePDB(ctx, buildZonePodDisruptionBudget(cortex, component, zone)); err != nil {
			return false, fmt.Errorf("reconciling %s zone %s PDB: %w", component, zone, err)
		}
	}

	statefulSets := buildComponentStatefulSets(cortex, component, configData)

	// Phase 1: Create any missing StatefulSets (non-sequential).
	created := map[string]bool{}
	for _, desired := range statefulSets {
		existing := &appsv1.StatefulSet{}
		err := r.Get(ctx, client.ObjectKeyFromObject(desired), existing)
		if apierrors.IsNotFound(err) {
			if err := r.Create(ctx, desired); err != nil {
				return false, fmt.Errorf("creating %s zone StatefulSet %s: %w", component, desired.Name, err)
			}
			created[desired.Name] = true
			continue
		}
		if err != nil {
			return false, fmt.Errorf("getting %s zone StatefulSet %s: %w", component, desired.Name, err)
		}
	}

	// Phase 2: Sequential updates — one zone at a time.
	// Skip StatefulSets that were just created in Phase 1.
	for _, desired := range statefulSets {
		if created[desired.Name] {
			continue
		}

		existing := &appsv1.StatefulSet{}
		if err := r.Get(ctx, client.ObjectKeyFromObject(desired), existing); err != nil {
			return false, fmt.Errorf("getting %s zone StatefulSet %s: %w", component, desired.Name, err)
		}

		// Check if the StatefulSet needs an update by comparing the fields we manage.
		// We avoid comparing full specs because the API server adds defaults
		// (probe timeouts, volume defaultMode, security contexts, etc.) that
		// would cause spurious updates on every reconciliation.
		if statefulSetNeedsUpdate(existing, desired) {
			// Preserve immutable VolumeClaimTemplates.
			desired.Spec.VolumeClaimTemplates = existing.Spec.VolumeClaimTemplates
			existing.Spec = desired.Spec
			existing.Labels = desired.Labels
			if err := r.Update(ctx, existing); err != nil {
				return false, fmt.Errorf("updating %s zone StatefulSet %s: %w", component, desired.Name, err)
			}
			logger.Info("Updated zone StatefulSet, waiting for rollout", "component", component, "statefulset", desired.Name)
			return true, nil
		}

		if !isStatefulSetRolledOut(existing) {
			logger.Info("Waiting for zone StatefulSet rollout", "component", component, "statefulset", existing.Name)
			return true, nil
		}
	}

	// Clean up old non-zone StatefulSet, PDB, and headless service.
	if err := r.cleanupNonZoneStatefulResources(ctx, cortex, component); err != nil {
		return false, fmt.Errorf("cleaning up non-zone %s resources: %w", component, err)
	}

	return false, nil
}

// reconcileNonZoneStatefulComponent reconciles a single StatefulSet (non-zone mode).
func (r *CortexReconciler) reconcileNonZoneStatefulComponent(ctx context.Context, cortex *cortexv1alpha1.Cortex, component, configData string) (bool, error) {
	if err := r.reconcileService(ctx, buildHeadlessService(cortex, component)); err != nil {
		return false, fmt.Errorf("reconciling %s headless service: %w", component, err)
	}

	statefulSets := buildComponentStatefulSets(cortex, component, configData)

	for _, sts := range statefulSets {
		if err := r.reconcileStatefulSet(ctx, sts); err != nil {
			return false, fmt.Errorf("reconciling %s StatefulSet: %w", component, err)
		}
	}

	if err := r.reconcilePDB(ctx, buildPodDisruptionBudget(cortex, component)); err != nil {
		return false, fmt.Errorf("reconciling %s PDB: %w", component, err)
	}

	// Clean up zone-specific resources if zone awareness was previously enabled.
	if err := r.cleanupZoneStatefulResources(ctx, cortex, component); err != nil {
		return false, fmt.Errorf("cleaning up zone %s resources: %w", component, err)
	}

	return false, nil
}

// cleanupNonZoneStatefulResources removes the single (non-zone) StatefulSet and PDB
// for a component, used when switching to zone-aware mode.
func (r *CortexReconciler) cleanupNonZoneStatefulResources(ctx context.Context, cortex *cortexv1alpha1.Cortex, component string) error {
	// Delete the non-zone StatefulSet.
	sts := &appsv1.StatefulSet{}
	stsKey := types.NamespacedName{Name: resourceName(cortex, component), Namespace: cortex.Namespace}
	if err := r.Get(ctx, stsKey, sts); err == nil {
		if err := r.Delete(ctx, sts); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("deleting non-zone StatefulSet %s: %w", stsKey.Name, err)
		}
	} else if !apierrors.IsNotFound(err) {
		return err
	}

	// Delete the non-zone PDB.
	pdb := &policyv1.PodDisruptionBudget{}
	pdbKey := types.NamespacedName{Name: resourceName(cortex, component), Namespace: cortex.Namespace}
	if err := r.Get(ctx, pdbKey, pdb); err == nil {
		if err := r.Delete(ctx, pdb); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("deleting non-zone PDB %s: %w", pdbKey.Name, err)
		}
	} else if !apierrors.IsNotFound(err) {
		return err
	}

	return nil
}

// cleanupZoneStatefulResources removes zone-specific StatefulSets, PDBs, and headless
// services for a component, used when switching from zone-aware to non-zone mode.
func (r *CortexReconciler) cleanupZoneStatefulResources(ctx context.Context, cortex *cortexv1alpha1.Cortex, component string) error {
	zoneLabel := labels.SelectorFromSet(map[string]string{
		LabelInstance:  cortex.Name,
		LabelComponent: component,
	})

	// List and delete zone StatefulSets.
	stsList := &appsv1.StatefulSetList{}
	if err := r.List(ctx, stsList, client.InNamespace(cortex.Namespace), client.MatchingLabelsSelector{Selector: zoneLabel}); err != nil {
		return err
	}
	for i := range stsList.Items {
		if _, hasZone := stsList.Items[i].Labels[LabelZone]; hasZone {
			if err := r.Delete(ctx, &stsList.Items[i]); err != nil && !apierrors.IsNotFound(err) {
				return err
			}
		}
	}

	// List and delete zone PDBs.
	pdbList := &policyv1.PodDisruptionBudgetList{}
	if err := r.List(ctx, pdbList, client.InNamespace(cortex.Namespace), client.MatchingLabelsSelector{Selector: zoneLabel}); err != nil {
		return err
	}
	for i := range pdbList.Items {
		if _, hasZone := pdbList.Items[i].Labels[LabelZone]; hasZone {
			if err := r.Delete(ctx, &pdbList.Items[i]); err != nil && !apierrors.IsNotFound(err) {
				return err
			}
		}
	}

	// List and delete zone headless services.
	svcList := &corev1.ServiceList{}
	if err := r.List(ctx, svcList, client.InNamespace(cortex.Namespace), client.MatchingLabelsSelector{Selector: zoneLabel}); err != nil {
		return err
	}
	for i := range svcList.Items {
		if _, hasZone := svcList.Items[i].Labels[LabelZone]; hasZone {
			if err := r.Delete(ctx, &svcList.Items[i]); err != nil && !apierrors.IsNotFound(err) {
				return err
			}
		}
	}

	return nil
}

// getConfigData generates or fetches the Cortex configuration YAML.
func (r *CortexReconciler) getConfigData(ctx context.Context, cortex *cortexv1alpha1.Cortex) (string, error) {
	if cortex.Spec.ExternalConfig != nil {
		// Fetch config from referenced ConfigMap.
		cm := &corev1.ConfigMap{}
		key := types.NamespacedName{
			Name:      cortex.Spec.ExternalConfig.ConfigMapRef.Name,
			Namespace: cortex.Namespace,
		}
		if err := r.Get(ctx, key, cm); err != nil {
			return "", fmt.Errorf("fetching external config ConfigMap %q: %w", key.Name, err)
		}
		configKey := cortex.Spec.ExternalConfig.Key
		if configKey == "" {
			configKey = "cortex.yaml"
		}
		data, ok := cm.Data[configKey]
		if !ok {
			return "", fmt.Errorf("key %q not found in external config ConfigMap %q", configKey, key.Name)
		}
		return data, nil
	}

	builder := config.NewBuilder(cortex.Name, cortex.Namespace, &cortex.Spec)
	return builder.Build()
}

// reconcileConfigMap creates or updates the ConfigMap.
func (r *CortexReconciler) reconcileConfigMap(ctx context.Context, cortex *cortexv1alpha1.Cortex, configData string) error {
	desired := buildConfigMap(cortex, configData)

	existing := &corev1.ConfigMap{}
	err := r.Get(ctx, types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}, existing)
	if apierrors.IsNotFound(err) {
		return r.Create(ctx, desired)
	}
	if err != nil {
		return err
	}

	if !equality.Semantic.DeepEqual(existing.Data, desired.Data) {
		existing.Data = desired.Data
		existing.Labels = desired.Labels
		return r.Update(ctx, existing)
	}

	return nil
}

// reconcileService creates or updates a Service.
func (r *CortexReconciler) reconcileService(ctx context.Context, desired *corev1.Service) error {
	existing := &corev1.Service{}
	err := r.Get(ctx, types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}, existing)
	if apierrors.IsNotFound(err) {
		return r.Create(ctx, desired)
	}
	if err != nil {
		return err
	}

	// Preserve ClusterIP on update.
	desired.Spec.ClusterIP = existing.Spec.ClusterIP
	if !equality.Semantic.DeepEqual(existing.Spec, desired.Spec) {
		existing.Spec = desired.Spec
		existing.Labels = desired.Labels
		return r.Update(ctx, existing)
	}

	return nil
}

// reconcileDeployment creates or updates a Deployment.
func (r *CortexReconciler) reconcileDeployment(ctx context.Context, desired *appsv1.Deployment) error {
	existing := &appsv1.Deployment{}
	err := r.Get(ctx, types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}, existing)
	if apierrors.IsNotFound(err) {
		return r.Create(ctx, desired)
	}
	if err != nil {
		return err
	}

	if !equality.Semantic.DeepEqual(existing.Spec, desired.Spec) {
		existing.Spec = desired.Spec
		existing.Labels = desired.Labels
		return r.Update(ctx, existing)
	}

	return nil
}

// reconcileStatefulSet creates or updates a StatefulSet.
func (r *CortexReconciler) reconcileStatefulSet(ctx context.Context, desired *appsv1.StatefulSet) error {
	existing := &appsv1.StatefulSet{}
	err := r.Get(ctx, types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}, existing)
	if apierrors.IsNotFound(err) {
		return r.Create(ctx, desired)
	}
	if err != nil {
		return err
	}

	// StatefulSet VolumeClaimTemplates are immutable after creation, so only update the rest.
	desired.Spec.VolumeClaimTemplates = existing.Spec.VolumeClaimTemplates

	if !equality.Semantic.DeepEqual(existing.Spec, desired.Spec) {
		existing.Spec = desired.Spec
		existing.Labels = desired.Labels
		return r.Update(ctx, existing)
	}

	return nil
}

// reconcilePDB creates or updates a PodDisruptionBudget.
func (r *CortexReconciler) reconcilePDB(ctx context.Context, desired *policyv1.PodDisruptionBudget) error {
	existing := &policyv1.PodDisruptionBudget{}
	err := r.Get(ctx, types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}, existing)
	if apierrors.IsNotFound(err) {
		return r.Create(ctx, desired)
	}
	if err != nil {
		return err
	}

	if !equality.Semantic.DeepEqual(existing.Spec, desired.Spec) {
		existing.Spec = desired.Spec
		existing.Labels = desired.Labels
		return r.Update(ctx, existing)
	}

	return nil
}

// updateStatus updates the Cortex CR status with component statuses and conditions.
func (r *CortexReconciler) updateStatus(ctx context.Context, cortex *cortexv1alpha1.Cortex) error {
	components := map[string]cortexv1alpha1.ComponentStatus{}

	// Gather Deployment statuses.
	for _, component := range []string{ComponentDistributor, ComponentQuerier, ComponentQueryFrontend} {
		dep := &appsv1.Deployment{}
		key := types.NamespacedName{Name: resourceName(cortex, component), Namespace: cortex.Namespace}
		if err := r.Get(ctx, key, dep); err != nil {
			if !apierrors.IsNotFound(err) {
				return err
			}
			continue
		}
		components[component] = cortexv1alpha1.ComponentStatus{
			Replicas:        dep.Status.Replicas,
			ReadyReplicas:   dep.Status.ReadyReplicas,
			UpdatedReplicas: dep.Status.UpdatedReplicas,
		}
	}

	// Gather StatefulSet statuses.
	// For zone-aware components (ingester, store-gateway, compactor), aggregate across all zone StatefulSets.
	for _, component := range []string{ComponentIngester, ComponentStoreGateway, ComponentCompactor} {
		if cortex.Spec.IsZoneAwarenessEnabled() {
			status := r.aggregateZoneStatefulSetStatus(ctx, cortex, component)
			components[component] = status
		} else {
			sts := &appsv1.StatefulSet{}
			key := types.NamespacedName{Name: resourceName(cortex, component), Namespace: cortex.Namespace}
			if err := r.Get(ctx, key, sts); err != nil {
				if !apierrors.IsNotFound(err) {
					return err
				}
				continue
			}
			components[component] = cortexv1alpha1.ComponentStatus{
				Replicas:        sts.Status.Replicas,
				ReadyReplicas:   sts.Status.ReadyReplicas,
				UpdatedReplicas: sts.Status.UpdatedReplicas,
			}
		}
	}

	cortex.Status.Components = components
	cortex.Status.ObservedGeneration = cortex.Generation
	cortex.Status.Version = cortex.Spec.Image.Tag

	// Determine overall readiness.
	allReady := true
	anyDegraded := false
	for _, cs := range components {
		if cs.ReadyReplicas < cs.Replicas {
			allReady = false
		}
		if cs.ReadyReplicas == 0 && cs.Replicas > 0 {
			anyDegraded = true
		}
	}

	if allReady {
		r.setCondition(cortex, conditionTypeReady, metav1.ConditionTrue, "AllComponentsReady", "All components are ready")
		r.setCondition(cortex, conditionTypeDegraded, metav1.ConditionFalse, "AllComponentsReady", "All components are ready")
	} else if anyDegraded {
		r.setCondition(cortex, conditionTypeReady, metav1.ConditionFalse, "ComponentsDegraded", "One or more components have no ready replicas")
		r.setCondition(cortex, conditionTypeDegraded, metav1.ConditionTrue, "ComponentsDegraded", "One or more components have no ready replicas")
	} else {
		r.setCondition(cortex, conditionTypeReady, metav1.ConditionFalse, "ComponentsNotReady", "Not all component replicas are ready")
		r.setCondition(cortex, conditionTypeDegraded, metav1.ConditionFalse, "ComponentsPartiallyReady", "Components are partially ready")
	}

	return r.Status().Update(ctx, cortex)
}

// aggregateZoneStatefulSetStatus aggregates the status of per-zone StatefulSets for a component.
func (r *CortexReconciler) aggregateZoneStatefulSetStatus(ctx context.Context, cortex *cortexv1alpha1.Cortex, component string) cortexv1alpha1.ComponentStatus {
	var status cortexv1alpha1.ComponentStatus
	for _, zone := range cortex.Spec.ZoneAwareness.Zones {
		sts := &appsv1.StatefulSet{}
		key := types.NamespacedName{Name: zoneResourceName(cortex, component, zone), Namespace: cortex.Namespace}
		if err := r.Get(ctx, key, sts); err != nil {
			continue
		}
		status.Replicas += sts.Status.Replicas
		status.ReadyReplicas += sts.Status.ReadyReplicas
		status.UpdatedReplicas += sts.Status.UpdatedReplicas
	}
	return status
}

// setCondition sets a condition on the Cortex status.
func (r *CortexReconciler) setCondition(cortex *cortexv1alpha1.Cortex, condType string, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(&cortex.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		ObservedGeneration: cortex.Generation,
		Reason:             reason,
		Message:            message,
	})
}

// SetupWithManager sets up the controller with the Manager.
func (r *CortexReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&cortexv1alpha1.Cortex{}).
		Owns(&appsv1.Deployment{}).
		Owns(&appsv1.StatefulSet{}).
		Owns(&corev1.ConfigMap{}).
		Owns(&corev1.Service{}).
		Owns(&policyv1.PodDisruptionBudget{}).
		Named("cortex").
		Complete(r)
}
