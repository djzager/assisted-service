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
	"strconv"

	"github.com/go-logr/logr"
	routev1 "github.com/openshift/api/route/v1"
	adiiov1alpha1 "github.com/openshift/assisted-service/internal/controller/api/v1alpha1"
	conditionsv1 "github.com/openshift/custom-resource-status/conditions/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	// agentServiceConfigName is the one and only name for an AgentServiceConfig
	// supported in the cluster. Any others will be ignored.
	agentServiceConfigName = "agent"
	// filesystemPVCName is the name of the PVC created for assisted-service's filesystem.
	filesystemPVCName = "agent-filesystem"
	// databasePVCName is the name of the PVC created for postgresql.
	databasePVCName = "agent-database"

	name                                = "assisted-service"
	databaseName                        = "postgres"
	databaseSecretName                  = databaseName
	databasePort                  int32 = 5432
	servicePort                   int32 = 8090
	assistedServiceDeploymentName       = "assisted-service"

	// assistedServiceContainerName is the Name property of the assisted-service container
	assistedServiceContainerName string = "assisted-service"
	// databaseContainerName is the Name property of the postgres container
	databaseContainerName string = databaseName
)

// AgentServiceConfigReconciler reconciles a AgentServiceConfig object
type AgentServiceConfigReconciler struct {
	client.Client
	Log    logr.Logger
	Scheme *runtime.Scheme
	// the Namespace the operator is running in
	// TODO(djzager): This must be configured on the operator deployment (ie. in the CSV
	// as from the metadata.Namespace
	Namespace string
}

// +kubebuilder:rbac:groups=adi.io.my.domain,resources=agentserviceconfigs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=adi.io.my.domain,resources=agentserviceconfigs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=adi.io.my.domain,resources=agentserviceconfigs/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=route.openshift.io,resources=routes,verbs=get;list;watch;create;update;patch;delete

func (r *AgentServiceConfigReconciler) Reconcile(req ctrl.Request) (ctrl.Result, error) {
	ctx := context.Background()
	_ = r.Log.WithValues("agentserviceconfig", req.NamespacedName)

	instance := &adiiov1alpha1.AgentServiceConfig{}

	// We only support one AgentServiceConfig per cluster, and it must be called "agent". This prevents installing
	// AgentService more than once in the cluster.
	if req.NamespacedName.Name != agentServiceConfigName {
		r.Log.Info(fmt.Sprintf("Invalid name (%s). Only one AgentServiceConfig supported per cluster and must be named '%s'", req.NamespacedName.Name, agentServiceConfigName))
		return reconcile.Result{}, nil
	}

	if err := r.Get(ctx, req.NamespacedName, instance); err != nil {
		r.Log.Error(err, "Failed to get resource", req.NamespacedName)
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	for _, f := range []func(context.Context, *adiiov1alpha1.AgentServiceConfig) error{
		r.ensureFilesystemStorage,
		r.ensureDatabaseStorage,
		r.ensureAgentService,
		r.ensureAgentRoute,
		r.ensurePostgresSecret,
		r.ensureAssistedServiceDeployment,
	} {
		err := f(ctx, instance)
		if err != nil {
			r.Log.Error(err, "Failed reconcile")
			if statusErr := r.Status().Update(ctx, instance); statusErr != nil {
				r.Log.Error(err, "Failed to update status")
				return ctrl.Result{Requeue: true}, statusErr
			}
			return ctrl.Result{Requeue: true}, err
		}
	}

	conditionsv1.SetStatusCondition(&instance.Status.Conditions, conditionsv1.Condition{
		Type:    adiiov1alpha1.ConditionReconcileCompleted,
		Status:  corev1.ConditionTrue,
		Reason:  "ReconcileCompleted",
		Message: "AgentServiceConfig reconcile completed without error.",
	})
	return ctrl.Result{}, r.Status().Update(ctx, instance)
}

func (r *AgentServiceConfigReconciler) ensureFilesystemStorage(ctx context.Context, instance *adiiov1alpha1.AgentServiceConfig) error {
	pvc, mutateFn := r.newFilesystemPVC(instance)

	if result, err := controllerutil.CreateOrUpdate(ctx, r.Client, pvc, mutateFn); err != nil {
		conditionsv1.SetStatusCondition(&instance.Status.Conditions, conditionsv1.Condition{
			Type:    adiiov1alpha1.ConditionReconcileCompleted,
			Status:  corev1.ConditionFalse,
			Reason:  "StorageFailure",
			Message: "Failed to ensure filesystem storage: " + err.Error(),
		})
		return err
	} else if result != controllerutil.OperationResultNone {
		r.Log.Info("Filesystem storage created")
	}
	return nil
}

func (r *AgentServiceConfigReconciler) ensureDatabaseStorage(ctx context.Context, instance *adiiov1alpha1.AgentServiceConfig) error {
	pvc, mutateFn := r.newDatabasePVC(instance)

	if result, err := controllerutil.CreateOrUpdate(ctx, r.Client, pvc, mutateFn); err != nil {
		conditionsv1.SetStatusCondition(&instance.Status.Conditions, conditionsv1.Condition{
			Type:    adiiov1alpha1.ConditionReconcileCompleted,
			Status:  corev1.ConditionFalse,
			Reason:  "StorageFailure",
			Message: "Failed to ensure database storage: " + err.Error(),
		})
		return err
	} else if result != controllerutil.OperationResultNone {
		r.Log.Info("Database storage created")
	}
	return nil
}

func (r *AgentServiceConfigReconciler) ensureAgentService(ctx context.Context, instance *adiiov1alpha1.AgentServiceConfig) error {
	svc, mutateFn := r.newAgentService(instance)

	if result, err := controllerutil.CreateOrUpdate(ctx, r.Client, svc, mutateFn); err != nil {
		conditionsv1.SetStatusCondition(&instance.Status.Conditions, conditionsv1.Condition{
			Type:    adiiov1alpha1.ConditionReconcileCompleted,
			Status:  corev1.ConditionFalse,
			Reason:  "AgentServiceFailure",
			Message: "Failed to ensure agent service: " + err.Error(),
		})
		return err
	} else if result != controllerutil.OperationResultNone {
		r.Log.Info("Agent service created")
	}
	return nil
}

func (r *AgentServiceConfigReconciler) ensureAgentRoute(ctx context.Context, instance *adiiov1alpha1.AgentServiceConfig) error {
	route, mutateFn := r.newAgentRoute(instance)

	if result, err := controllerutil.CreateOrUpdate(ctx, r.Client, route, mutateFn); err != nil {
		conditionsv1.SetStatusCondition(&instance.Status.Conditions, conditionsv1.Condition{
			Type:    adiiov1alpha1.ConditionReconcileCompleted,
			Status:  corev1.ConditionFalse,
			Reason:  "AgentRouteFailure",
			Message: "Failed to ensure agent route: " + err.Error(),
		})
		return err
	} else if result != controllerutil.OperationResultNone {
		r.Log.Info("Agent route created")
	}
	return nil
}

func (r *AgentServiceConfigReconciler) ensurePostgresSecret(ctx context.Context, instance *adiiov1alpha1.AgentServiceConfig) error {
	secret, mutateFn := r.newPostgresSecret(instance)

	if result, err := controllerutil.CreateOrUpdate(ctx, r.Client, secret, mutateFn); err != nil {
		conditionsv1.SetStatusCondition(&instance.Status.Conditions, conditionsv1.Condition{
			Type:    adiiov1alpha1.ConditionReconcileCompleted,
			Status:  corev1.ConditionFalse,
			Reason:  "PostgresSecretFailure",
			Message: "Failed to ensure database secret: " + err.Error(),
		})
		return err
	} else if result != controllerutil.OperationResultNone {
		r.Log.Info("Database secret created")
	}
	return nil
}

func (r *AgentServiceConfigReconciler) ensureAssistedServiceDeployment(ctx context.Context, instance *adiiov1alpha1.AgentServiceConfig) error {
	deployment, mutateFn := r.newAssistedServiceDeployment(instance)

	if result, err := controllerutil.CreateOrUpdate(ctx, r.Client, deployment, mutateFn); err != nil {
		conditionsv1.SetStatusCondition(&instance.Status.Conditions, conditionsv1.Condition{
			Type:    adiiov1alpha1.ConditionReconcileCompleted,
			Status:  corev1.ConditionFalse,
			Reason:  "DeploymentFailure",
			Message: "Failed to ensure assisted service deployment: " + err.Error(),
		})
		return err
	} else if result != controllerutil.OperationResultNone {
		r.Log.Info("Assisted service deployment created")
	}
	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *AgentServiceConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&adiiov1alpha1.AgentServiceConfig{}).
		Owns(&corev1.PersistentVolumeClaim{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.Secret{}).
		Owns(&routev1.Route{}).
		Owns(&appsv1.Deployment{}).
		Complete(r)
}

func (r *AgentServiceConfigReconciler) newFilesystemPVC(instance *adiiov1alpha1.AgentServiceConfig) (*corev1.PersistentVolumeClaim, controllerutil.MutateFn) {
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-filesystem", agentServiceConfigName),
			Namespace: r.Namespace,
		},
		Spec: instance.Spec.FileSystemStorage,
	}

	mutateFn := func() error {
		if err := controllerutil.SetControllerReference(instance, pvc, r.Scheme); err != nil {
			return err
		}
		// Everything else is immutable once bound.
		pvc.Spec.Resources.Requests = instance.Spec.DatabaseStorage.Resources.Requests
		return nil
	}

	return pvc, mutateFn
}

func (r *AgentServiceConfigReconciler) newDatabasePVC(instance *adiiov1alpha1.AgentServiceConfig) (*corev1.PersistentVolumeClaim, controllerutil.MutateFn) {
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-database", agentServiceConfigName),
			Namespace: r.Namespace,
		},
		Spec: instance.Spec.DatabaseStorage,
	}

	mutateFn := func() error {
		if err := controllerutil.SetControllerReference(instance, pvc, r.Scheme); err != nil {
			return err
		}
		// Everything else is immutable once bound.
		pvc.Spec.Resources.Requests = instance.Spec.DatabaseStorage.Resources.Requests
		return nil
	}

	return pvc, mutateFn
}

func (r *AgentServiceConfigReconciler) newAgentService(instance *adiiov1alpha1.AgentServiceConfig) (*corev1.Service, controllerutil.MutateFn) {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: r.Namespace,
		},
	}

	mutateFn := func() error {
		if err := controllerutil.SetControllerReference(instance, svc, r.Scheme); err != nil {
			return err
		}
		addAppLabel(name, &svc.ObjectMeta)
		if len(svc.Spec.Ports) == 0 {
			svc.Spec.Ports = append(svc.Spec.Ports, corev1.ServicePort{})
		}
		// For convenience targetPort, when unset, is set to the same as port
		// https://kubernetes.io/docs/concepts/services-networking/service/#defining-a-service
		// so we don't set it.
		svc.Spec.Ports[0].Name = name
		svc.Spec.Ports[0].Port = servicePort
		svc.Spec.Ports[0].Protocol = corev1.ProtocolTCP
		svc.Spec.Selector = map[string]string{"app": name}
		svc.Spec.Type = corev1.ServiceTypeLoadBalancer
		return nil
	}

	return svc, mutateFn
}

func (r *AgentServiceConfigReconciler) newAgentRoute(instance *adiiov1alpha1.AgentServiceConfig) (*routev1.Route, controllerutil.MutateFn) {
	weight := int32(100)
	route := &routev1.Route{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: r.Namespace,
		},
	}
	routeSpec := routev1.RouteSpec{
		To: routev1.RouteTargetReference{
			Kind:   "Service",
			Name:   name,
			Weight: &weight,
		},
		Port: &routev1.RoutePort{
			TargetPort: intstr.FromString(name),
		},
		WildcardPolicy: routev1.WildcardPolicyNone,
	}

	mutateFn := func() error {
		if err := controllerutil.SetControllerReference(instance, route, r.Scheme); err != nil {
			return err
		}
		route.Spec = routeSpec
		return nil
	}

	return route, mutateFn
}

func (r *AgentServiceConfigReconciler) newPostgresSecret(instance *adiiov1alpha1.AgentServiceConfig) (*corev1.Secret, controllerutil.MutateFn) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      databaseSecretName,
			Namespace: r.Namespace,
		},
	}

	// pass, err := generatePassword()
	// if err != nil {
	// 	return nil, err
	// }

	// TODO(djzager): Use legit password
	secretData := map[string]string{
		"db.host":     "localhost",
		"db.user":     "admin",
		"db.password": "abcdefg",
		"db.name":     "installer",
		"db.port":     strconv.Itoa(int(databasePort)),
	}

	secretType := corev1.SecretTypeOpaque

	mutateFn := func() error {
		if err := controllerutil.SetControllerReference(instance, secret, r.Scheme); err != nil {
			return err
		}
		secret.StringData = secretData
		secret.Type = secretType
		return nil
	}

	return secret, mutateFn
}

func (r *AgentServiceConfigReconciler) newAssistedServiceDeployment(instance *adiiov1alpha1.AgentServiceConfig) (*appsv1.Deployment, controllerutil.MutateFn) {
	ocmSSOSecretName := "assisted-installer-sso"
	s3SecretName := "assisted-installer-s3"
	publicS3SecretName := "assisted-installer-public-s3"
	assistedServiceConfigMapName := "assisted-service-config"
	maxUnavailable := intstr.FromString("50%")
	maxSurge := intstr.FromString("100%")

	serviceContainer := corev1.Container{
		Name:  assistedServiceContainerName,
		Image: ServiceImage(),
		Ports: []corev1.ContainerPort{
			{
				ContainerPort: servicePort,
				Protocol:      corev1.ProtocolTCP,
			},
		},
		Env: []corev1.EnvVar{
			// TODO(djzager): clean up unneccessary environment variables
			newSecretEnvVar("OCM_SERVICE_CLIENT_ID", "ocm-service.clientId", ocmSSOSecretName),
			newSecretEnvVar("OCM_SERVICE_CLIENT_SECRET", "ocm-service.clientSecret", ocmSSOSecretName),
			newSecretEnvVar("AWS_SECRET_ACCESS_KEY", "aws_secret_access_key", s3SecretName),
			newSecretEnvVar("AWS_ACCESS_KEY_ID", "aws_access_key_id", s3SecretName),
			newSecretEnvVar("S3_REGION", "aws_region", s3SecretName),
			newSecretEnvVar("S3_BUCKET", "bucket", s3SecretName),
			newSecretEnvVar("S3_ENDPOINT_URL", "endpoint", s3SecretName),
			newSecretEnvVar("AWS_SECRET_ACCESS_KEY_PUBLIC", "aws_secret_access_key", publicS3SecretName),
			newSecretEnvVar("AWS_ACCESS_KEY_ID_PUBLIC", "aws_access_key_id", publicS3SecretName),
			newSecretEnvVar("S3_REGION_PUBLIC", "aws_region", publicS3SecretName),
			newSecretEnvVar("S3_BUCKET_PUBLIC", "bucket", publicS3SecretName),
			newSecretEnvVar("S3_ENDPOINT_URL_PUBLIC", "endpoint", publicS3SecretName),

			// TODO: Should these be in a ConfigMap? the assisted-service-config CM?
			newEnvVar("ISO_IMAGE_TYPE", "minimal-iso"),
			newEnvVar("S3_USE_SSL", "false"),
			newEnvVar("LOG_LEVEL", "info"),
			newEnvVar("LOG_FORMAT", "text"),
			newEnvVar("INSTALL_RH_CA", "false"),
			newEnvVar("REGISTRY_CREDS", ""),
			newEnvVar("AWS_SHARED_CREDENTIALS_FILE", "/etc/.aws/credentials"),
			newEnvVar("DEPLOY_TARGET", "ocp"),
			newEnvVar("STORAGE", "filesystem"),
			newEnvVar("ISO_WORKSPACE_BASE_DIR", "/data"),
			newEnvVar("ISO_CACHE_DIR", "/data/cache"),

			// database
			newSecretEnvVar("DB_HOST", "db.host", databaseSecretName),
			newSecretEnvVar("DB_NAME", "db.name", databaseSecretName),
			newSecretEnvVar("DB_PASS", "db.password", databaseSecretName),
			newSecretEnvVar("DB_PORT", "db.port", databaseSecretName),
			newSecretEnvVar("DB_USER", "db.user", databaseSecretName),

			// image overrides
			newEnvVar("AGENT_DOCKER_IMAGE", AgentImage()),
			newEnvVar("CONTROLLER_IMAGE", ControllerImage()),
			newEnvVar("INSTALLER_IMAGE", InstallerImage()),
			newEnvVar("SELF_VERSION", ServiceImage()),

			{
				Name: "NAMESPACE",
				ValueFrom: &corev1.EnvVarSource{
					FieldRef: &corev1.ObjectFieldSelector{
						FieldPath: "metadata.namespace",
					},
				},
			},
		},
		EnvFrom: []corev1.EnvFromSource{
			{
				ConfigMapRef: &corev1.ConfigMapEnvSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: assistedServiceConfigMapName,
					},
				},
			},
		},
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      "bucket-filesystem",
				MountPath: "/data",
			},
		},
		Resources: corev1.ResourceRequirements{
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    *resource.NewMilliQuantity(500, resource.DecimalSI),
				corev1.ResourceMemory: *resource.NewQuantity(2000*1024*1024, resource.BinarySI),
			},
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    *resource.NewMilliQuantity(300, resource.DecimalSI),
				corev1.ResourceMemory: *resource.NewQuantity(400*1024*1024, resource.BinarySI),
			},
		},
		LivenessProbe: &corev1.Probe{
			FailureThreshold:    3,
			SuccessThreshold:    1,
			InitialDelaySeconds: 3,
			PeriodSeconds:       10,
			TimeoutSeconds:      3,
			Handler: corev1.Handler{
				TCPSocket: &corev1.TCPSocketAction{
					Port: intstr.FromInt(int(servicePort)),
				},
			},
		},
	}

	postgresContainer := corev1.Container{
		Name:            databaseContainerName,
		Image:           DatabaseImage(),
		ImagePullPolicy: corev1.PullIfNotPresent,
		Ports: []corev1.ContainerPort{
			{
				Name:          databaseName,
				ContainerPort: databasePort,
				Protocol:      corev1.ProtocolTCP,
			},
		},
		Env: []corev1.EnvVar{
			newSecretEnvVar("POSTGRESQL_DATABASE", "db.name", databaseSecretName),
			newSecretEnvVar("POSTGRESQL_USER", "db.user", databaseSecretName),
			newSecretEnvVar("POSTGRESQL_PASSWORD", "db.password", databaseSecretName),
		},
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      "postgresdb",
				MountPath: "/var/lib/pgsql/data",
			},
		},
		Resources: corev1.ResourceRequirements{
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    *resource.NewMilliQuantity(200, resource.DecimalSI),
				corev1.ResourceMemory: *resource.NewQuantity(500*1024*1024, resource.BinarySI),
			},
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    *resource.NewMilliQuantity(100, resource.DecimalSI),
				corev1.ResourceMemory: *resource.NewQuantity(400*1024*1024, resource.BinarySI),
			},
		},
	}

	volumes := []corev1.Volume{
		{
			Name: "bucket-filesystem",
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: filesystemPVCName,
				},
			},
		},
		{
			Name: "postgresdb",
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: databasePVCName,
				},
			},
		},
	}

	deploymentLabels := map[string]string{
		"app": assistedServiceDeploymentName,
	}

	deploymentStrategy := appsv1.DeploymentStrategy{
		Type: appsv1.RollingUpdateDeploymentStrategyType,
		RollingUpdate: &appsv1.RollingUpdateDeployment{
			MaxUnavailable: &maxUnavailable,
			MaxSurge:       &maxSurge,
		},
	}

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      assistedServiceDeploymentName,
			Namespace: r.Namespace,
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: deploymentLabels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: deploymentLabels,
					Name:   assistedServiceDeploymentName,
				},
			},
		},
	}

	mutateFn := func() error {
		if err := controllerutil.SetControllerReference(instance, deployment, r.Scheme); err != nil {
			return err
		}
		var replicas int32 = 1
		deployment.Spec.Replicas = &replicas
		deployment.Spec.Strategy = deploymentStrategy
		deployment.Spec.Template.Spec.Containers = []corev1.Container{serviceContainer, postgresContainer}
		deployment.Spec.Template.Spec.Volumes = volumes

		return nil
	}
	return deployment, mutateFn
}

func newEnvVar(name, value string) corev1.EnvVar {
	return corev1.EnvVar{
		Name:  name,
		Value: value,
	}
}

func newSecretEnvVar(name, key, secretName string) corev1.EnvVar {
	return corev1.EnvVar{
		Name: name,
		ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{
				Key: key,
				LocalObjectReference: corev1.LocalObjectReference{
					Name: secretName,
				},
			},
		},
	}
}
