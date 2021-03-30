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
	"reflect"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	// "sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	// "sigs.k8s.io/controller-runtime/pkg/source"

	routev1 "github.com/openshift/api/route/v1"
	adiiov1alpha1 "github.com/openshift/assisted-service/internal/controller/api/v1alpha1"
	conditionsv1 "github.com/openshift/custom-resource-status/conditions/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
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
	databasePort                  int32 = 5432
	assistedServiceDeploymentName       = "assisted-service"
	postgresDeploymentName              = databaseName

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

	// We only support one AgentServiceConfig per cluster, and it must be called "hive". This prevents installing
	// AgentService more than once in the cluster.
	if req.NamespacedName.Name != agentServiceConfigName {
		err := fmt.Errorf("Invalid name (%s) expected %s", req.NamespacedName.Name, agentServiceConfigName)
		r.Log.Error(err, fmt.Sprintf("Only one AgentServiceConfig supported per cluster and must be named '%s'", agentServiceConfigName))
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
		r.ensurePostgresDeployment,
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

	return ctrl.Result{}, nil
}

func (r *AgentServiceConfigReconciler) ensureFilesystemStorage(ctx context.Context, instance *adiiov1alpha1.AgentServiceConfig) error {
	pvc := r.newFilesystemPVC(instance)
	if err := controllerutil.SetControllerReference(instance, pvc, r.Scheme); err != nil {
		return err
	}
	if err := r.ensureStorage(ctx, instance, pvc); err != nil {
		conditionsv1.SetStatusCondition(&instance.Status.Conditions, conditionsv1.Condition{
			Type:    adiiov1alpha1.FilesystemStorageCreated,
			Status:  corev1.ConditionFalse,
			Reason:  "StorageFailure",
			Message: "Failed to ensure filesystem storage: " + err.Error(),
		})
		return err
	}
	conditionsv1.SetStatusCondition(&instance.Status.Conditions, conditionsv1.Condition{
		Type:    adiiov1alpha1.FilesystemStorageCreated,
		Status:  corev1.ConditionTrue,
		Reason:  "StorageCreated",
		Message: "Filesystem storage created",
	})
	return nil
}

func (r *AgentServiceConfigReconciler) ensureDatabaseStorage(ctx context.Context, instance *adiiov1alpha1.AgentServiceConfig) error {
	pvc := r.newDatabasePVC(instance)
	if err := controllerutil.SetControllerReference(instance, pvc, r.Scheme); err != nil {
		return err
	}

	if err := r.ensureStorage(ctx, instance, pvc); err != nil {
		conditionsv1.SetStatusCondition(&instance.Status.Conditions, conditionsv1.Condition{
			Type:    adiiov1alpha1.DatabaseStorageCreated,
			Status:  corev1.ConditionFalse,
			Reason:  "StorageFailure",
			Message: "Failed to ensure filesystem storage: " + err.Error(),
		})
		return err
	}
	conditionsv1.SetStatusCondition(&instance.Status.Conditions, conditionsv1.Condition{
		Type:    adiiov1alpha1.DatabaseStorageCreated,
		Status:  corev1.ConditionTrue,
		Reason:  "StorageCreated",
		Message: "Database storage created",
	})
	return nil
}

func (r *AgentServiceConfigReconciler) ensureStorage(ctx context.Context, instance *adiiov1alpha1.AgentServiceConfig, pvc *corev1.PersistentVolumeClaim) error {
	found := &corev1.PersistentVolumeClaim{}
	if err := r.Client.Get(ctx, types.NamespacedName{Name: pvc.Name, Namespace: pvc.Namespace}, found); err != nil {
		if apierrors.IsNotFound(err) {
			r.Log.Info("Creating PVC", "Namespace", pvc.Namespace, "Name", pvc.Name)
			err := r.Client.Create(ctx, pvc)
			if err != nil {
				r.Log.Error(err, "Create PVC failed", "Namespace", pvc.Namespace, "Name", pvc.Name)
			}
			return err
		}
		r.Log.Error(err, "Get PVC failed", "Namespace", pvc.Namespace, "Name", pvc.Name)
		return err
	}

	// TODO(djzager): Move to controllerutil.CreateOrupdate()
	if !reflect.DeepEqual(pvc.Spec, found.Spec) {
		found.Spec = pvc.Spec
		return r.Client.Update(ctx, found)
	}

	return nil
}

func (r *AgentServiceConfigReconciler) ensureAgentService(ctx context.Context, instance *adiiov1alpha1.AgentServiceConfig) error {
	return errors.New("Not implemented")
}

func (r *AgentServiceConfigReconciler) ensureAgentRoute(ctx context.Context, instance *adiiov1alpha1.AgentServiceConfig) error {
	return errors.New("Not implemented")
}

func (r *AgentServiceConfigReconciler) ensurePostgresSecret(ctx context.Context, instance *adiiov1alpha1.AgentServiceConfig) error {
	return errors.New("Not implemented")
}

func (r *AgentServiceConfigReconciler) ensureAssistedServiceDeployment(ctx context.Context, instance *adiiov1alpha1.AgentServiceConfig) error {
	deployment := r.newAssistedServiceDeployment(instance)
	if err := controllerutil.SetControllerReference(instance, deployment, r.Scheme); err != nil {
		return err
	}

	if err := r.ensureDeployment(ctx, instance, deployment); err != nil {
		conditionsv1.SetStatusCondition(&instance.Status.Conditions, conditionsv1.Condition{
			Type:    adiiov1alpha1.ServiceDeploymentCreated,
			Status:  corev1.ConditionFalse,
			Reason:  "FailedCreate",
			Message: "Failed to ensure assisted-service deployment: " + err.Error(),
		})
		return err
	}

	conditionsv1.SetStatusCondition(&instance.Status.Conditions, conditionsv1.Condition{
		Type:    adiiov1alpha1.ServiceDeploymentCreated,
		Status:  corev1.ConditionTrue,
		Reason:  "DeploymentCreated",
		Message: "Assisted service deployment created",
	})
	return nil
}

func (r *AgentServiceConfigReconciler) ensurePostgresDeployment(ctx context.Context, instance *adiiov1alpha1.AgentServiceConfig) error {
	deployment := r.newPostgresDeployment(instance)
	if err := controllerutil.SetControllerReference(instance, deployment, r.Scheme); err != nil {
		return err
	}

	if err := r.ensureDeployment(ctx, instance, deployment); err != nil {
		conditionsv1.SetStatusCondition(&instance.Status.Conditions, conditionsv1.Condition{
			Type:    adiiov1alpha1.DatabaseDeploymentCreated,
			Status:  corev1.ConditionFalse,
			Reason:  "FailedCreate",
			Message: "Failed to ensure database deployment: " + err.Error(),
		})
		return err
	}

	conditionsv1.SetStatusCondition(&instance.Status.Conditions, conditionsv1.Condition{
		Type:    adiiov1alpha1.DatabaseDeploymentCreated,
		Status:  corev1.ConditionTrue,
		Reason:  "DeploymentCreated",
		Message: "Database deployment created",
	})
	return nil
}

func (r *AgentServiceConfigReconciler) ensureDeployment(ctx context.Context, instance *adiiov1alpha1.AgentServiceConfig, deployment *appsv1.Deployment) error {
	found := &appsv1.Deployment{}
	if err := r.Client.Get(ctx, types.NamespacedName{Name: deployment.Name, Namespace: deployment.Namespace}, found); err != nil {
		if apierrors.IsNotFound(err) {
			r.Log.Info("Creating Deployment", "Namespace", deployment.Namespace, "Name", deployment.Name)
			err := r.Client.Create(ctx, deployment)
			if err != nil {
				r.Log.Error(err, "Create deployment failed", "Namespace", deployment.Namespace, "Name", deployment.Name)
				return err
			}
			return nil
		}
		r.Log.Error(err, "Get deployment failed", "Namespace", deployment.Namespace, "Name", deployment.Name)
		return err
	}

	// TODO(djzager): Move to controllerutil.CreateOrupdate()
	if !reflect.DeepEqual(deployment.Spec, found.Spec) {
		found.Spec = deployment.Spec
		return r.Client.Update(ctx, found)
	}

	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *AgentServiceConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&adiiov1alpha1.AgentServiceConfig{}).
		Owns(&corev1.PersistentVolumeClaim{}).
		Owns(&appsv1.Deployment{}).
		Complete(r)
}

func (r *AgentServiceConfigReconciler) newFilesystemPVC(instance *adiiov1alpha1.AgentServiceConfig) *corev1.PersistentVolumeClaim {
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-filesystem", agentServiceConfigName),
			Namespace: r.Namespace,
		},
		Spec: instance.Spec.FileSystemStorage,
	}
}

func (r *AgentServiceConfigReconciler) newDatabasePVC(instance *adiiov1alpha1.AgentServiceConfig) *corev1.PersistentVolumeClaim {
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-database", agentServiceConfigName),
			Namespace: r.Namespace,
		},
		Spec: instance.Spec.DatabaseStorage,
	}
}

func (r *AgentServiceConfigReconciler) newAgentService(instance *adiiov1alpha1.AgentServiceConfig) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: r.Namespace,
			Labels: map[string]string{
				"app": name,
			},
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{
					Name: name,
					Port: 8090,
					Protocol: corev1.ProtocolTCP,
					TargetPort: intstr.FromInt(8090),
				},
			},
			Selector: map[string]string{
				"app": name,
			},
			Type: corev1.ServiceTypeLoadBalancer,
		},
	}
}

func (r *AgentServiceConfigReconciler) newAgentRoute(instance *adiiov1alpha1.AgentServiceConfig) *routev1.Route {
	weight := int32(100)
	return &routev1.Route{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: r.Namespace,
		},
		Spec: routev1.RouteSpec{
			To: routev1.RouteTargetReference{
				Kind: "Service",
				Name: name,
				Weight: &weight,
			},
			Port: &routev1.RoutePort{
				TargetPort: intstr.FromString(name),
			},
			WildcardPolicy: routev1.WildcardPolicyNone,
		},
	}
}

func (r *AgentServiceConfigReconciler) newPostgresSecret(instance *adiiov1alpha1.AgentServiceConfig) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-secret", databaseName),
			Namespace: r.Namespace,
		},
		// TODO(djzager): Create legit password
		StringData: map[string]string{
			"db.host":     databaseName,
			"db.user":     "admin",
			"db.password": "abcdefg",
			"db.name":     "installer",
			"db.port":     string(databasePort),
		},
		Type: corev1.SecretTypeOpaque,
	}
}

func (r *AgentServiceConfigReconciler) newAssistedServiceDeployment(instance *adiiov1alpha1.AgentServiceConfig) *appsv1.Deployment {
	postgresSecretName := "assisted-installer-rds"
	ocmSSOSecretName := "assisted-installer-sso"
	s3SecretName := "assisted-installer-s3"
	publicS3SecretName := "assisted-installer-public-s3"
	assistedServiceConfigMapName := "assisted-service-config"
	maxUnavailable := intstr.FromString("50%")
	maxSurge := intstr.FromString("100%")
	optionalFlag := true
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      assistedServiceDeploymentName,
			Namespace: r.Namespace,
		},
		Spec: appsv1.DeploymentSpec{
			// Replicas: &instance.Spec.Replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app": assistedServiceDeploymentName,
				},
			},
			Strategy: appsv1.DeploymentStrategy{
				Type: appsv1.RollingUpdateDeploymentStrategyType,
				RollingUpdate: &appsv1.RollingUpdateDeployment{
					MaxUnavailable: &maxUnavailable,
					MaxSurge:       &maxSurge,
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app":        assistedServiceDeploymentName,
						"deployment": assistedServiceDeploymentName,
					},
					Annotations: map[string]string{},
				},
				Spec: corev1.PodSpec{
					Volumes: []corev1.Volume{
						{
							Name: "configs",
							VolumeSource: corev1.VolumeSource{
								Secret: &corev1.SecretVolumeSource{
									Optional:   &optionalFlag,
									SecretName: "route53-creds",
								},
							},
						},
						{
							Name: "bucket-filesystem",
							VolumeSource: corev1.VolumeSource{
								PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
									ClaimName: filesystemPVCName,
								},
							},
						},
					},
					Containers: []corev1.Container{
						{
							Name:            assistedServiceContainerName,
							Image:           ServiceImage(),
							ImagePullPolicy: corev1.PullAlways,
							Ports: []corev1.ContainerPort{
								{
									Name:          "assisted-service",
									ContainerPort: 8090,
									Protocol:      corev1.ProtocolTCP,
								},
							},
							Env: []corev1.EnvVar{
								newSecretEnvVar("DB_HOST", "db.host", postgresSecretName),
								newSecretEnvVar("DB_NAME", "db.name", postgresSecretName),
								newSecretEnvVar("DB_PASS", "db.password", postgresSecretName),
								newSecretEnvVar("DB_PORT", "db.port", postgresSecretName),
								newSecretEnvVar("DB_USER", "db.user", postgresSecretName),
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
									Name:      "route53-creds",
									ReadOnly:  true,
									MountPath: "/etc/.aws",
								},
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
										Port: intstr.FromInt(8090),
									},
								},
							},
						},
					},
					InitContainers: []corev1.Container{
						{
							Name: "init-wait-for-service",
							// TODO: replace image value
							Image: "registry.access.redhat.com/ubi8/ubi-minimal:latest",
							Command: []string{
								"sh",
							},
							Args: []string{
								"-c",
								"until getent hosts assisted-service.$(cat /var/run/secrets/kubernetes.io/serviceaccount/namespace).svc.cluster.local; do echo waiting for assisted-service; sleep 2; done",
							},
						},
						{
							Name: "init-add-route-and-update-config-map",
							// TODO: replace image value
							Image: "quay.io/openshift/origin-cli@sha256:3a931dd86a2cbbec8c96740bfe3d5b8da78f50d66597579a1d6d2e4916adecad",
							Command: []string{
								"sh",
							},
							Args: []string{
								"-c",
								"export NAMESPACE=$(cat /var/run/secrets/kubernetes.io/serviceaccount/namespace) && export HAS_ROUTE=$(oc get routes -n $NAMESPACE assisted-service) && if [ \"$HAS_ROUTE\" == \"\" ] ; then oc expose service -n $NAMESPACE assisted-service ; fi && export ROUTE_URL=$(oc get routes -n $NAMESPACE assisted-service -o jsonpath={.spec.host}) && oc get configmap -n $NAMESPACE assisted-service-config -o yaml | sed -e \"s|REPLACE_BASE_URL|http\\:\\/\\/$ROUTE_URL|\" | oc apply -f - ",
							},
						},
					},
				},
			},
		},
	}
	return dep
}

func (r *AgentServiceConfigReconciler) newPostgresDeployment(instance *adiiov1alpha1.AgentServiceConfig) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      databasePVCName,
			Namespace: r.Namespace,
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app": postgresDeploymentName,
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app":        postgresDeploymentName,
						"deployment": postgresDeploymentName,
					},
					Annotations: map[string]string{},
				},
				Spec: corev1.PodSpec{
					Volumes: []corev1.Volume{
						{
							Name: "postgresdb",
							VolumeSource: corev1.VolumeSource{
								PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
									ClaimName: databasePVCName,
								},
							},
						},
					},
					Containers: []corev1.Container{
						{
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
								// TODO(djzager): look into hardcoded secret
								newSecretEnvVar("POSTRESQL_DATABASE", "db.name", "assisted-installer-rds"),
								newSecretEnvVar("POSTRESQL_USER", "db.user", "assisted-installer-rds"),
								newSecretEnvVar("POSTRESQL_PASSWORD", "db.password", "assisted-installer-rds"),
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
						},
					},
					InitContainers: []corev1.Container{
						{
							Name: "init-add-postgres-secret-if-missing",
							// TODO: replace image value
							Image: "quay.io/openshift/origin-cli:latest",
							Command: []string{
								"sh",
							},
							Args: []string{
								"-c",
								"export NAMESPACE=$(cat /var/run/secrets/kubernetes.io/serviceaccount/namespace) && export HAS_PG_SECRET=$(oc get secrets -n $NAMESPACE assisted-installer-rds) && if [ \"$HAS_PG_SECRET\" == \"\" ] ; then export PASSWORD=$(cat /dev/urandom | tr -dc \"a-zA-Z0-9\" | head -c 20) && oc create secret generic assisted-installer-rds --from-literal=db.host=postgres --from-literal=db.name=installer --from-literal=db.port=5432 --from-literal=db.user=admin --type=opaque --from-literal=db.password=$PASSWORD -n $NAMESPACE; fi",
							},
						},
					},
				},
			},
		},
	}
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
