package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"

	"github.com/RHEcosystemAppEng/dbaas-operator/api/v1alpha1"

	"github.com/go-logr/logr"

	"k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

// InstallNamespaceEnvVar is the constant for env variable INSTALL_NAMESPACE
var InstallNamespaceEnvVar = "INSTALL_NAMESPACE"
var inventoryNamespaceKey = ".spec.inventoryNamespace"

type DBaaSReconciler struct {
	client.Client
	*runtime.Scheme
	InstallNamespace string
}

func (r *DBaaSReconciler) getDBaaSProvider(providerName string, ctx context.Context) (*v1alpha1.DBaaSProvider, error) {
	provider := &v1alpha1.DBaaSProvider{}
	if err := r.Get(ctx, types.NamespacedName{Name: providerName}, provider); err != nil {
		return nil, err
	}
	return provider, nil
}

func (r *DBaaSReconciler) watchDBaaSProviderObject(ctrl controller.Controller, object runtime.Object, providerObjectKind string) error {
	providerObject := unstructured.Unstructured{}
	providerObject.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   v1alpha1.GroupVersion.Group,
		Version: v1alpha1.GroupVersion.Version,
		Kind:    providerObjectKind,
	})
	err := ctrl.Watch(
		&source.Kind{
			Type: &providerObject,
		},
		&handler.EnqueueRequestForOwner{
			OwnerType:    object,
			IsController: true,
		},
	)
	if err != nil {
		return err
	}
	return nil
}

func (r *DBaaSReconciler) createProviderObject(object client.Object, providerObjectKind string) *unstructured.Unstructured {
	var providerObject unstructured.Unstructured
	providerObject.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   v1alpha1.GroupVersion.Group,
		Version: v1alpha1.GroupVersion.Version,
		Kind:    providerObjectKind,
	})
	providerObject.SetNamespace(object.GetNamespace())
	providerObject.SetName(object.GetName())
	return &providerObject
}

func (r *DBaaSReconciler) providerObjectMutateFn(object client.Object, providerObject *unstructured.Unstructured, spec interface{}) controllerutil.MutateFn {
	return func() error {
		providerObject.UnstructuredContent()["spec"] = spec
		providerObject.SetOwnerReferences(nil)
		if err := ctrl.SetControllerReference(object, providerObject, r.Scheme); err != nil {
			return err
		}
		return nil
	}
}

func (r *DBaaSReconciler) parseProviderObject(unstructured *unstructured.Unstructured, object interface{}) error {
	b, err := unstructured.MarshalJSON()
	if err != nil {
		return err
	}
	err = json.Unmarshal(b, object)
	if err != nil {
		return err
	}
	return nil
}

func (r *DBaaSReconciler) createOwnedObject(k8sObj, owner client.Object, ctx context.Context) error {
	if err := ctrl.SetControllerReference(owner, k8sObj, r.Scheme); err != nil {
		return err
	}
	if err := r.Create(ctx, k8sObj); err != nil {
		return err
	}
	return nil
}

func (r *DBaaSReconciler) updateObject(k8sObj client.Object, ctx context.Context) error {
	if err := r.Update(ctx, k8sObj); err != nil {
		return err
	}
	return nil
}

// populate Tenant List based on spec.inventoryNamespace
func (r *DBaaSReconciler) tenantListByInventoryNS(ctx context.Context, inventoryNamespace string) (v1alpha1.DBaaSTenantList, error) {
	var tenantListByNS v1alpha1.DBaaSTenantList
	if err := r.List(ctx, &tenantListByNS, client.MatchingFields{inventoryNamespaceKey: inventoryNamespace}); err != nil {
		return v1alpha1.DBaaSTenantList{}, err
	}
	return tenantListByNS, nil
}

func (r *DBaaSReconciler) reconcileProviderResource(providerName string, DBaaSObject client.Object,
	providerObjectKindFn func(*v1alpha1.DBaaSProvider) string, DBaaSObjectSpecFn func() interface{},
	providerObjectFn func() interface{}, DBaaSObjectSyncStatusFn func(interface{}) metav1.Condition,
	DBaaSObjectConditionsFn func() *[]metav1.Condition, DBaaSObjectReadyType string,
	ctx context.Context, logger logr.Logger) (result ctrl.Result, recErr error) {

	var condition *metav1.Condition
	if cond := apimeta.FindStatusCondition(*DBaaSObjectConditionsFn(), DBaaSObjectReadyType); cond != nil {
		condition = cond.DeepCopy()
	} else {
		condition = &metav1.Condition{
			Type:    DBaaSObjectReadyType,
			Status:  metav1.ConditionFalse,
			Reason:  v1alpha1.ProviderReconcileInprogress,
			Message: v1alpha1.MsgProviderCRReconcileInProgress,
		}
	}

	// This update will make sure the status is always updated in case of any errors or successful result
	defer func(cond *metav1.Condition) {
		apimeta.SetStatusCondition(DBaaSObjectConditionsFn(), *cond)
		if err := r.Client.Status().Update(ctx, DBaaSObject); err != nil {
			if errors.IsConflict(err) {
				logger.V(1).Info("DBaaS object modified, retry syncing status", "DBaaS Object", DBaaSObject)
				// Re-queue and preserve existing recErr
				result = ctrl.Result{Requeue: true}
				return
			}
			logger.Error(err, "Error updating the DBaaS resource status", "DBaaS Object", DBaaSObject)
			if recErr == nil {
				// There is no existing recErr. Set it to the status update error
				recErr = err
			}
		}
	}(condition)

	provider, err := r.getDBaaSProvider(providerName, ctx)
	if err != nil {
		recErr = err
		if errors.IsNotFound(err) {
			logger.Error(err, "Requested DBaaS Provider is not configured in this environment", "DBaaS Provider", providerName)
			*condition = metav1.Condition{Type: DBaaSObjectReadyType, Status: metav1.ConditionFalse, Reason: v1alpha1.DBaaSProviderNotFound, Message: err.Error()}
			return
		}
		logger.Error(err, "Error reading configured DBaaS Provider", "DBaaS Provider", providerName)
		return
	}
	logger.V(1).Info("Found DBaaS Provider", "DBaaS Provider", providerName)

	providerObject := r.createProviderObject(DBaaSObject, providerObjectKindFn(provider))
	if res, err := controllerutil.CreateOrUpdate(ctx, r.Client, providerObject, r.providerObjectMutateFn(DBaaSObject, providerObject, DBaaSObjectSpecFn())); err != nil {
		if errors.IsConflict(err) {
			logger.V(1).Info("Provider object modified, retry syncing spec", "Provider Object", providerObject)
			result = ctrl.Result{Requeue: true}
			return
		}
		logger.Error(err, "Error reconciling the Provider resource", "Provider Object", providerObject)
		recErr = err
		return
	} else {
		logger.V(1).Info("Provider resource reconciled", "Provider Object", providerObject, "result", res)
	}

	DBaaSProviderObject := providerObjectFn()
	if err := r.parseProviderObject(providerObject, DBaaSProviderObject); err != nil {
		logger.Error(err, "Error parsing the Provider object", "Provider Object", providerObject)
		*condition = metav1.Condition{Type: DBaaSObjectReadyType, Status: metav1.ConditionFalse, Reason: v1alpha1.ProviderParsingError, Message: err.Error()}
		recErr = err
		return
	}

	*condition = DBaaSObjectSyncStatusFn(DBaaSProviderObject)
	return
}

// update object upon ownerReference verification
func (r *DBaaSReconciler) updateIfOwned(ctx context.Context, owner, obj client.Object) error {
	logger := ctrl.LoggerFrom(ctx, owner.GetObjectKind().GroupVersionKind().Kind, owner.GetName())
	name := obj.GetName()
	kind := obj.GetObjectKind().GroupVersionKind().Kind
	if owns, err := isOwner(owner, obj, r.Scheme); !owns {
		logger.V(1).Info(kind+" ownership not verified, won't be updated", "Name", name)
		return err
	}
	if err := r.updateObject(obj, ctx); err != nil {
		logger.Error(err, "Error updating resource", "Name", name)
		return err
	}
	logger.Info(kind+" resource updated", "Name", name)
	return nil
}

// checks if one object is set as owner/controller of another
func isOwner(owner, ownedObj client.Object, scheme *runtime.Scheme) (owns bool, err error) {
	exampleObj := &unstructured.Unstructured{}
	exampleObj.SetNamespace(owner.GetNamespace())
	if err = ctrl.SetControllerReference(owner, exampleObj, scheme); err == nil {
		for _, ownerRef := range exampleObj.GetOwnerReferences() {
			for _, ref := range ownedObj.GetOwnerReferences() {
				if reflect.DeepEqual(ownerRef, ref) {
					owns = true
				}
			}
		}
	}
	return owns, err
}

// GetInstallNamespace returns the operator's install Namespace
func GetInstallNamespace() (string, error) {
	ns, found := os.LookupEnv(InstallNamespaceEnvVar)
	if !found {
		return "", fmt.Errorf("%s must be set", InstallNamespaceEnvVar)
	}
	return ns, nil
}

// checks if a string is present in a slice
func contains(s []string, str string) bool {
	for _, v := range s {
		if v == str {
			return true
		}
	}
	return false
}
