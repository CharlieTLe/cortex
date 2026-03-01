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

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

	// 3. Ingester (headless service + StatefulSet + PDB)
	if err := r.reconcileService(ctx, buildHeadlessService(cortex, ComponentIngester)); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconciling ingester headless service: %w", err)
	}
	if err := r.reconcileStatefulSet(ctx, buildIngesterStatefulSet(cortex, configData)); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconciling ingester StatefulSet: %w", err)
	}
	if err := r.reconcilePDB(ctx, buildPodDisruptionBudget(cortex, ComponentIngester)); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconciling ingester PDB: %w", err)
	}

	// 4. Store Gateway (headless service + StatefulSet + PDB)
	if err := r.reconcileService(ctx, buildHeadlessService(cortex, ComponentStoreGateway)); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconciling store-gateway headless service: %w", err)
	}
	if err := r.reconcileStatefulSet(ctx, buildStoreGatewayStatefulSet(cortex, configData)); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconciling store-gateway StatefulSet: %w", err)
	}
	if err := r.reconcilePDB(ctx, buildPodDisruptionBudget(cortex, ComponentStoreGateway)); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconciling store-gateway PDB: %w", err)
	}

	// 5. Distributor (Service + Deployment)
	if err := r.reconcileService(ctx, buildComponentService(cortex, ComponentDistributor)); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconciling distributor service: %w", err)
	}
	distributorSpec := cortex.Spec.Distributor
	if err := r.reconcileDeployment(ctx, buildDeployment(cortex, ComponentDistributor, distributorSpec, configData)); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconciling distributor deployment: %w", err)
	}

	// 6. Compactor (headless service + StatefulSet + PDB)
	if err := r.reconcileService(ctx, buildHeadlessService(cortex, ComponentCompactor)); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconciling compactor headless service: %w", err)
	}
	if err := r.reconcileService(ctx, buildComponentService(cortex, ComponentCompactor)); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconciling compactor service: %w", err)
	}
	if err := r.reconcileStatefulSet(ctx, buildCompactorStatefulSet(cortex, configData)); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconciling compactor StatefulSet: %w", err)
	}
	if err := r.reconcilePDB(ctx, buildPodDisruptionBudget(cortex, ComponentCompactor)); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconciling compactor PDB: %w", err)
	}

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

	return ctrl.Result{}, nil
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
	for _, component := range []string{ComponentIngester, ComponentStoreGateway, ComponentCompactor} {
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
