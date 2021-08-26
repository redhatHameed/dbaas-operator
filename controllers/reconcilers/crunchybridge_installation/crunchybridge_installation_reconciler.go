package crunchybridge_installation

import (
	"context"
	v1 "github.com/RHEcosystemAppEng/dbaas-operator/api/v1alpha1"
	"github.com/RHEcosystemAppEng/dbaas-operator/controllers/reconcilers"
	"github.com/go-logr/logr"
	coreosv1 "github.com/operator-framework/api/pkg/operators/v1"
	"github.com/operator-framework/api/pkg/operators/v1alpha1"
	apiv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	apimv1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

type Reconciler struct {
	client client.Client
	logger logr.Logger
	scheme *runtime.Scheme
}

func NewReconciler(client client.Client, scheme *runtime.Scheme, logger logr.Logger) reconcilers.PlatformReconciler {
	return &Reconciler{
		client: client,
		scheme: scheme,
		logger: logger,
	}
}

func (r *Reconciler) Reconcile(ctx context.Context, cr *v1.DBaaSPlatform, status2 *v1.DBaaSPlatformStatus) (v1.PlatformsInstlnStatus, error) {

	//crunchybridge CatalogSource
	status, err := r.reconcileCatalogSource(ctx)
	if status != v1.ResultSuccess {
		return status, err
	}

	// crunchybridge subscription
	status, err = r.reconcileSubscription(ctx)
	if status != v1.ResultSuccess {
		return status, err
	}
	// crunchybridge operator group
	status, err = r.reconcileOperatorgroup(ctx)
	if status != v1.ResultSuccess {
		return status, err
	}
	status, err = r.waitForCrunchyBridgeOperator(ctx)
	if status != v1.ResultSuccess {
		return status, err
	}
	return v1.ResultSuccess, nil

}
func (r *Reconciler) Cleanup(ctx context.Context, cr *v1.DBaaSPlatform) (v1.PlatformsInstlnStatus, error) {

	subscription := GetCrunchyBridgeSubscription()
	err := r.client.Delete(ctx, subscription)
	if err != nil && !errors.IsNotFound(err) {
		return v1.ResultFailed, err
	}

	catalogSource := GetCrunchyBridgeCatalogSource()
	err = r.client.Delete(ctx, catalogSource)
	if err != nil && !errors.IsNotFound(err) {
		return v1.ResultFailed, err
	}
	deployments := &apiv1.DeploymentList{}
	opts := &client.ListOptions{
		Namespace: reconcilers.INSTALL_NAMESPACE,
	}
	err = r.client.List(ctx, deployments, opts)
	if err != nil {
		return v1.ResultFailed, err
	}

	for _, deployment := range deployments.Items {
		if deployment.Name == "crunchy-bridge-operator-controller-manager" {
			err = r.client.Delete(ctx, &deployment)
			if err != nil && !errors.IsNotFound(err) {
				return v1.ResultFailed, err
			}
		}
	}

	return v1.ResultSuccess, nil
}

func (r *Reconciler) reconcileSubscription(ctx context.Context) (v1.PlatformsInstlnStatus, error) {

	subscription := GetCrunchyBridgeSubscription()
	catalogsource := GetCrunchyBridgeCatalogSource()
	_, err := controllerutil.CreateOrUpdate(ctx, r.client, subscription, func() error {
		subscription.Spec = &v1alpha1.SubscriptionSpec{
			CatalogSource:          catalogsource.Name,
			CatalogSourceNamespace: catalogsource.Namespace,
			Package:                "crunchy-bridge-operator",
			Channel:                "alpha",
			InstallPlanApproval:    v1alpha1.ApprovalAutomatic,
		}

		return nil
	})

	if err != nil {
		return v1.ResultFailed, err
	}
	return v1.ResultSuccess, nil
}
func (r *Reconciler) reconcileOperatorgroup(ctx context.Context) (v1.PlatformsInstlnStatus, error) {

	operatorgroup := GetCrunchyBridgeOperatorGroup()
	_, err := controllerutil.CreateOrUpdate(ctx, r.client, operatorgroup, func() error {
		operatorgroup.Spec = coreosv1.OperatorGroupSpec{
			//TargetNamespaces: []string{"openshift-operators"},

		}

		return nil
	})
	if err != nil {
		return v1.ResultFailed, err
	}

	return v1.ResultSuccess, nil
}
func (r *Reconciler) reconcileCatalogSource(ctx context.Context) (v1.PlatformsInstlnStatus, error) {
	catalogsource := GetCrunchyBridgeCatalogSource()
	_, err := controllerutil.CreateOrUpdate(ctx, r.client, catalogsource, func() error {
		catalogsource.Spec = v1alpha1.CatalogSourceSpec{
			SourceType:  v1alpha1.SourceTypeGrpc,
			Image:       reconcilers.CRUNCHY_BRIDGE_CATLOG_IMG,
			DisplayName: "Crunchy Bridge Operator",
		}
		return nil
	})
	if err != nil {
		return v1.ResultFailed, err
	}
	return v1.ResultSuccess, nil
}

func (r *Reconciler) waitForCrunchyBridgeOperator(ctx context.Context) (v1.PlatformsInstlnStatus, error) {
	// We have to remove the prometheus operator deployment manually
	deployments := &apiv1.DeploymentList{}
	opts := &client.ListOptions{
		//Namespace: cr.Namespace,
		Namespace: reconcilers.INSTALL_NAMESPACE,
	}
	err := r.client.List(ctx, deployments, opts)
	if err != nil {
		return v1.ResultFailed, err
	}

	for _, deployment := range deployments.Items {
		if deployment.Name == "crunchy-bridge-operator-controller-manager" {
			if deployment.Status.ReadyReplicas > 0 {
				return v1.ResultSuccess, nil
			}
		}
	}
	return v1.ResultInProgress, nil
}

func GetCrunchyBridgeSubscription() *v1alpha1.Subscription {
	return &v1alpha1.Subscription{
		ObjectMeta: apimv1.ObjectMeta{
			Name:      "crunchy-bridge-subscription",
			Namespace: reconcilers.INSTALL_NAMESPACE,
		},
	}
}
func GetCrunchyBridgeOperatorGroup() *coreosv1.OperatorGroup {
	return &coreosv1.OperatorGroup{
		ObjectMeta: apimv1.ObjectMeta{
			Name:      "global-operators",
			Namespace: reconcilers.INSTALL_NAMESPACE,
		},
	}
}

func GetCrunchyBridgeCatalogSource() *v1alpha1.CatalogSource {
	return &v1alpha1.CatalogSource{
		ObjectMeta: apimv1.ObjectMeta{
			Name:      "crunchy-bridge-catalogsource",
			Namespace: reconcilers.CATALOG_NAMESPACE,
		},
	}
}
