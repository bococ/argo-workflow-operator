/*
Copyright 2023.

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
	"github.com/go-logr/logr"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"

	stackv1alpha1 "github.com/zncdata-labs/argo-workflow-operator/api/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ArgoWorkFlowReconciler reconciles a ArgoWorkFlow object
type ArgoWorkFlowReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Log    logr.Logger
}

// +kubebuilder:rbac:groups=stack.zncdata.net,resources=argoworkflows,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=stack.zncdata.net,resources=argoworkflows/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=stack.zncdata.net,resources=argoworkflows/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=serviceaccounts,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;

// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims;persistentvolumeclaims/finalizers,verbs=get;create;update;delete
// +kubebuilder:rbac:groups="",resources=pods;pods/exec,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=argoproj.io,resources=workflows;workflows/finalizers;workflowtasksets;workflowtasksets/finalizers;workflowartifactgctasks,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=argoproj.io,resources=workflowtemplates;workflowtemplates/finalizers,verbs=get;list;watch
// +kubebuilder:rbac:groups=argoproj.io,resources=cronworkflows;cronworkflows/finalizers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=argoproj.io,resources=workflowtaskresults,verbs=list;watch;deletecollection
// +kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=get;list
// +kubebuilder:rbac:groups="policy",resources=poddisruptionbudgets,verbs=create;get;delete
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=create
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,resourceNames=workflow-controller;workflow-controller-lease,verbs=get;list;watch;create;update;patch;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the ArgoWorkFlow object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.14.1/pkg/reconcile
func (r *ArgoWorkFlowReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {

	r.Log.Info("Reconciling ArgoWorkFlow")

	argoWorkflow := &stackv1alpha1.ArgoWorkFlow{}

	if err := r.Get(ctx, req.NamespacedName, argoWorkflow); err != nil {
		if client.IgnoreNotFound(err) != nil {
			r.Log.Error(err, "unable to fetch ArgoWorkFlow")
			return ctrl.Result{}, err
		}
		r.Log.Info("ArgoWorkFlow resource not found. Ignoring since object must be deleted")
		return ctrl.Result{}, nil
	}

	// Get the status condition, if it exists and its generation is not the
	//same as the ArgoWorkFlow's generation, reset the status conditions
	readCondition := apimeta.FindStatusCondition(argoWorkflow.Status.Conditions, stackv1alpha1.ConditionTypeProgressing)
	if readCondition == nil || readCondition.ObservedGeneration != argoWorkflow.GetGeneration() {
		argoWorkflow.InitStatusConditions()

		if err := r.UpdateStatus(ctx, argoWorkflow); err != nil {
			return ctrl.Result{}, err
		}
	}

	r.Log.Info("ArgoWorkFlow found", "Name", argoWorkflow.Name)

	if err := r.reconcileDeployment(ctx, argoWorkflow); err != nil {
		r.Log.Error(err, "unable to reconcile Deployment")
		return ctrl.Result{}, err
	}

	if err := r.reconcileService(ctx, argoWorkflow); err != nil {
		r.Log.Error(err, "unable to reconcile Service")
		return ctrl.Result{}, err
	}

	if err := r.reconcileServiceAccount(ctx, argoWorkflow); err != nil {
		r.Log.Error(err, "unable to reconcile ServiceAccount")
		return ctrl.Result{}, err
	}

	if err := r.reconcileClusterRoleBinding(ctx, argoWorkflow); err != nil {
		r.Log.Error(err, "unable to reconcile ClusterRoleBinding")
		return ctrl.Result{}, err
	}

	if err := r.reconcileConfigMap(ctx, argoWorkflow); err != nil {
		r.Log.Error(err, "unable to reconcile ConfigMap")
		return ctrl.Result{}, err
	}

	argoWorkflow.SetStatusCondition(metav1.Condition{
		Type:               stackv1alpha1.ConditionTypeAvailable,
		Status:             metav1.ConditionTrue,
		Reason:             stackv1alpha1.ConditionReasonRunning,
		Message:            "ArgoWorkFlow is running",
		ObservedGeneration: argoWorkflow.GetGeneration(),
	})

	if err := r.UpdateStatus(ctx, argoWorkflow); err != nil {
		return ctrl.Result{}, err
	}

	r.Log.Info("Successfully reconciled ArgoWorkFlow")
	return ctrl.Result{}, nil
}

// UpdateStatus updates the status of the ArgoWorkFlow resource
// https://stackoverflow.com/questions/76388004/k8s-controller-update-status-and-condition
func (r *ArgoWorkFlowReconciler) UpdateStatus(ctx context.Context, instance *stackv1alpha1.ArgoWorkFlow) error {
	retryErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		return r.Status().Update(ctx, instance)
		//return r.Status().Patch(ctx, instance, client.MergeFrom(instance))
	})

	if retryErr != nil {
		r.Log.Error(retryErr, "Failed to update vfm status after retries")
		return retryErr
	}

	r.Log.V(1).Info("Successfully patched object status")
	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ArgoWorkFlowReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&stackv1alpha1.ArgoWorkFlow{}).
		Complete(r)
}
