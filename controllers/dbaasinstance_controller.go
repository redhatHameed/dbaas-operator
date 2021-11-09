/*
Copyright 2021.

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

package controllers

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller"

	"github.com/RHEcosystemAppEng/dbaas-operator/api/v1alpha1"
)

// DBaaSInstanceReconciler reconciles a DBaaSInstance object
type DBaaSInstanceReconciler struct {
	*DBaaSReconciler
}

//+kubebuilder:rbac:groups=dbaas.redhat.com,resources=dbaasinstances,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=dbaas.redhat.com,resources=dbaasinstances/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=dbaas.redhat.com,resources=dbaasinstances/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.9.2/pkg/reconcile
func (r *DBaaSInstanceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := ctrl.LoggerFrom(ctx, "DBaaS Instance", req.NamespacedName)

	var instance v1alpha1.DBaaSInstance
	if err := r.Get(ctx, req.NamespacedName, &instance); err != nil {
		if errors.IsNotFound(err) {
			// CR deleted since request queued, child objects getting GC'd, no requeue
			logger.Info("DBaaS Instance resource not found, has been deleted")
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Error fetching DBaaS Instance for reconcile")
		return ctrl.Result{}, err
	}

	var inventory v1alpha1.DBaaSInventory
	if err := r.Get(ctx, types.NamespacedName{Namespace: instance.Spec.InventoryRef.Namespace, Name: instance.Spec.InventoryRef.Name}, &inventory); err != nil {
		if errors.IsNotFound(err) {
			logger.Error(err, "DBaaS Inventory resource not found for DBaaS Instance", "DBaaS Instance", instance, "DBaaS Inventory", instance.Spec.InventoryRef)
			cond := metav1.Condition{
				Type:    v1alpha1.DBaaSInstanceReadyType,
				Status:  metav1.ConditionFalse,
				Reason:  v1alpha1.DBaaSInventoryNotFound,
				Message: err.Error(),
			}
			apimeta.SetStatusCondition(&instance.Status.Conditions, cond)
			errCond := r.Client.Status().Update(ctx, &instance)
			if errCond != nil {
				if errors.IsConflict(errCond) {
					logger.V(1).Info("DBaaS Instance resource modified", "DBaaS Instance", instance)
				}
				logger.Error(errCond, "Error updating the DBaaS Instance resource status", "DBaaS Instance", instance)
			}
			return ctrl.Result{}, err
		}
		logger.Error(err, "Error fetching DBaaS Inventory resource reference for DBaaS Instance", "DBaaS Inventory", instance.Spec.InventoryRef)
		return ctrl.Result{}, err
	}

	// The inventory must be in ready status before we can move on
	invCond := apimeta.FindStatusCondition(inventory.Status.Conditions, v1alpha1.DBaaSInventoryReadyType)
	if invCond == nil || invCond.Status == metav1.ConditionFalse {
		err := fmt.Errorf("inventory %v is not ready", instance.Spec.InventoryRef)
		logger.Error(err, "Inventory is not ready", "Inventory", inventory.Name, "Namespace", inventory.Namespace)
		cond := metav1.Condition{
			Type:    v1alpha1.DBaaSInstanceReadyType,
			Status:  metav1.ConditionFalse,
			Reason:  v1alpha1.DBaaSInventoryNotReady,
			Message: v1alpha1.MsgInventoryNotReady,
		}
		apimeta.SetStatusCondition(&instance.Status.Conditions, cond)
		errCond := r.Client.Status().Update(ctx, &instance)
		if errCond != nil {
			if errors.IsConflict(errCond) {
				logger.V(1).Info("DBaaS Instance resource modified", "DBaaS Instance", instance)
			}
			logger.Error(errCond, "Error updating the DBaaS Instance resource status", "DBaaS Instance", instance)
		}
		return ctrl.Result{}, err
	}

	return r.reconcileProviderResource(inventory.Spec.ProviderRef.Name,
		&instance,
		func(provider *v1alpha1.DBaaSProvider) string {
			return provider.Spec.InstanceKind
		},
		func() interface{} {
			return instance.Spec.DeepCopy()
		},
		func() interface{} {
			return &v1alpha1.DBaaSProviderInstance{}
		},
		func(i interface{}) metav1.Condition {
			providerInstance := i.(*v1alpha1.DBaaSProviderInstance)
			return mergeInstanceStatus(&instance, providerInstance)
		},
		func() *[]metav1.Condition {
			return &instance.Status.Conditions
		},
		v1alpha1.DBaaSInstanceReadyType,
		ctx,
		logger,
	)
}

// SetupWithManager sets up the controller with the Manager.
func (r *DBaaSInstanceReconciler) SetupWithManager(mgr ctrl.Manager) (controller.Controller, error) {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.DBaaSInstance{}).
		Build(r)
}

// mergeInstanceStatus: merge the status from DBaaSProviderInstance into the current DBaaSInstance status
func mergeInstanceStatus(instance *v1alpha1.DBaaSInstance, providerInst *v1alpha1.DBaaSProviderInstance) metav1.Condition {
	providerInst.Status.DeepCopyInto(&instance.Status)
	// Update instance status condition (type: DBaaSInstanceReadyType) based on the provider status
	specSync := apimeta.FindStatusCondition(providerInst.Status.Conditions, v1alpha1.DBaaSInstanceProviderSyncType)
	if specSync != nil && specSync.Status == metav1.ConditionTrue {
		return metav1.Condition{
			Type:    v1alpha1.DBaaSInstanceReadyType,
			Status:  metav1.ConditionTrue,
			Reason:  v1alpha1.Ready,
			Message: v1alpha1.MsgProviderCRStatusSyncDone,
		}
	}
	return metav1.Condition{
		Type:    v1alpha1.DBaaSInstanceReadyType,
		Status:  metav1.ConditionFalse,
		Reason:  v1alpha1.ProviderReconcileInprogress,
		Message: v1alpha1.MsgProviderCRReconcileInProgress,
	}
}
