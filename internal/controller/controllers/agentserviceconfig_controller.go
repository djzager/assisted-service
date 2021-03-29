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
	"errors"
	"fmt"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	adiiov1alpha1 "github.com/openshift/assisted-service/internal/controller/api/v1alpha1"
	// conditionsv1 "github.com/openshift/custom-resource-status/conditions/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
)

const (
	// agentServiceConfigName is the one and only name for an AgentServiceConfig
	// supported in the cluster. Any others will be ignored.
	agentServiceConfigName = "agent"
)

// AgentServiceConfigReconciler reconciles a AgentServiceConfig object
type AgentServiceConfigReconciler struct {
	client.Client
	Log    logr.Logger
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=adi.io.my.domain,resources=agentserviceconfigs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=adi.io.my.domain,resources=agentserviceconfigs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=adi.io.my.domain,resources=agentserviceconfigs/finalizers,verbs=update

func (r *AgentServiceConfigReconciler) Reconcile(req ctrl.Request) (ctrl.Result, error) {
	ctx := context.Background()
	_ = r.Log.WithValues("agentserviceconfig", req.NamespacedName)

	instance := &adiiov1alpha1.AgentServiceConfig{}

	// We only support one AgentServiceConfig per cluster, and it must be called "hive". This prevents installing
	// AgentService more than once in the cluster.
	if req.NamespacedName.Name != agentServiceConfigName {
		err := fmt.Errorf("Invalid name (%s) expected %s", req.NamespacedName.Name, agentServiceConfigName)
		r.Log.Error(err, fmt.Sprintf("Only one AgentServiceConfig supported per cluster. and must be named '%s'", agentServiceConfigName))
		return reconcile.Result{}, nil
	}

	if err := r.Get(ctx, req.NamespacedName, instance); err != nil {
		r.Log.Error(err, "Failed to get resource", req.NamespacedName)
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	for _, f := range []func(*adiiov1alpha1.AgentServiceConfig) error{
		r.ensureFilesystemStorage,
		r.ensureDatabaseStorage,
	} {
		err := f(instance)
		if err != nil {
			r.Log.Error(err, "Failed reconcile")
			if statusErr := r.Update(ctx, instance); statusErr != nil {
				r.Log.Error(err, "Failed to update status")
				return ctrl.Result{Requeue: true}, statusErr
			}
			return ctrl.Result{Requeue: true}, nil
		}
	}

	return ctrl.Result{}, nil
}

func (r *AgentServiceConfigReconciler) ensureFilesystemStorage(instance *adiiov1alpha1.AgentServiceConfig) error {
	return errors.New("function not implemented")
}

func (r *AgentServiceConfigReconciler) ensureDatabaseStorage(instance *adiiov1alpha1.AgentServiceConfig) error {
	return errors.New("function not implemented")
}

// SetupWithManager sets up the controller with the Manager.
func (r *AgentServiceConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&adiiov1alpha1.AgentServiceConfig{}).
		Watches(
			&source.Kind{Type: &corev1.PersistentVolumeClaim{}},
			&handler.EnqueueRequestForOwner{
				OwnerType:    &adiiov1alpha1.AgentServiceConfig{},
				IsController: true,
			},
		).
		Watches(
			&source.Kind{Type: &appsv1.Deployment{}},
			&handler.EnqueueRequestForOwner{
				OwnerType:    &adiiov1alpha1.AgentServiceConfig{},
				IsController: true,
			},
		).
		Complete(r)
}
