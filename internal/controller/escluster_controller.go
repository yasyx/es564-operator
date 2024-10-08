/*
Copyright 2024.

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
	"crypto/md5"
	"fmt"
	"gopkg.in/yaml.v3"
	appv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/json"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	elasticsearchv1alpha1 "github.com/yasyx/es564-operator/api/v1alpha1"
)

// ESClusterReconciler reconciles a ESCluster object
type ESClusterReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

const EsStatefulSetName = "es564-ss"
const EsHeadlessServiceName = "es564-headless-svc"
const EsConfigmapName = "es564-cm"
const EsConfigSumAnnotation = "elasticsearch.yasy.run/config-sum"

//+kubebuilder:rbac:groups=elasticsearch.yasy.run,resources=esclusters,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=elasticsearch.yasy.run,resources=esclusters/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=elasticsearch.yasy.run,resources=esclusters/finalizers,verbs=update
//+kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=core,resources=configmaps,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the ESCluster object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.17.3/pkg/reconcile
func (r *ESClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Starting reconciling ESCluster ....")

	// if the ESCluster crd not exists, return and do nothing
	esCluster := &elasticsearchv1alpha1.ESCluster{}
	if err := r.Get(ctx, req.NamespacedName, esCluster); err != nil {
		if errors.IsNotFound(err) {
			logger.Info("ESCluster crd not found")
			return ctrl.Result{}, nil
		}
		logger.Error(err, "unable to fetch ESCluster")
		return ctrl.Result{}, err
	}
	// get esCluster size and name
	clusterSize := esCluster.Spec.Size
	clusterName := esCluster.Spec.ClusterName
	logger.Info("esCluster info:", "clusterSize", clusterSize, "clusterName", clusterName)

	// create or update esConfigMap
	configMap := corev1.ConfigMap{}
	err := r.Get(ctx, types.NamespacedName{Name: EsConfigmapName, Namespace: esCluster.Namespace}, &configMap)
	if err != nil && errors.IsNotFound(err) {
		res, err := createOrUpdateEsConfigMap(ctx, r, esCluster, true)
		if err != nil {
			logger.Error(err, "unable to create esConfigMap")
			return res, err
		}
	} else if err != nil {
		logger.Error(err, "unable to fetch ConfigMap")
		return ctrl.Result{}, err
	} else {
		res, err := createOrUpdateEsConfigMap(ctx, r, esCluster, false)
		if err != nil {
			logger.Error(err, "unable to update esConfigMap")
			return res, err
		}
	}

	marshal, err := json.Marshal(configMap.Data)
	if err != nil {
		return ctrl.Result{}, err
	}
	configSum := fmt.Sprintf("%x", md5.Sum(marshal))

	// create headless service for es cluster
	service := corev1.Service{}
	err = r.Get(ctx, types.NamespacedName{Name: EsHeadlessServiceName, Namespace: esCluster.Namespace}, &service)
	if err != nil && errors.IsNotFound(err) {
		if res, err := createHeadlessService(ctx, r, esCluster); err != nil {
			logger.Error(err, "create elasticsearch headless service err !!!")
			return res, err
		}
	}

	// create or update statefulSet for es cluster
	statefulSet := appv1.StatefulSet{}
	err = r.Get(ctx, types.NamespacedName{Name: EsStatefulSetName, Namespace: esCluster.Namespace}, &statefulSet)
	if err != nil && errors.IsNotFound(err) {
		if res, err := createStatefulSet(ctx, r, esCluster, configSum); err != nil {
			logger.Error(err, "create elasticsearch statefulList err !!!")
			return res, err
		}
	} else if err != nil {
		logger.Error(err, "fetch elasticsearch stateful err !!!")
	} else {
		statefulSet.Spec.Replicas = &esCluster.Spec.Size
		annotations := statefulSet.Spec.Template.Annotations
		if annotations == nil {
			annotations = make(map[string]string)
		}
		if annotations[EsConfigSumAnnotation] != configSum {
			annotations[EsConfigSumAnnotation] = configSum
			statefulSet.Spec.Template.Annotations = annotations
			if err := r.Update(ctx, &statefulSet); err != nil {
				logger.Error(err, "update elasticsearch stateful err !!!")
				return ctrl.Result{}, err
			}
		}
	}

	logger.Info("End reconciling EsCluster....")

	return ctrl.Result{}, nil
}

func createOrUpdateEsConfigMap(ctx context.Context, r *ESClusterReconciler, esCluster *elasticsearchv1alpha1.ESCluster, create bool) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	clusterName := esCluster.Spec.ClusterName
	size := esCluster.Spec.Size
	var discoveryZenPing []string
	dataMap := make(map[string]string)
	for i := 0; i < int(size); i++ {
		dataMapKey := fmt.Sprintf("elasticsearch-%d.yml", i)
		headlessSvc := fmt.Sprintf("%s-%d.%s.%s.svc.cluster.local", EsStatefulSetName, i, EsHeadlessServiceName, esCluster.Namespace)

		esConfig := ESConfig{}
		config := esConfig.NewESConfig()
		config.setClusterName(clusterName)
		config.setNodeName(headlessSvc)
		config.setNetworkPublishHost(headlessSvc)
		discoveryZenPing = append(discoveryZenPing, fmt.Sprintf("%s:9300", headlessSvc))
		config.setDiscoveryZenPing(discoveryZenPing)
		marshal, err := yaml.Marshal(config)
		if err != nil {
			return ctrl.Result{}, err
		}
		dataMap[dataMapKey] = string(marshal)
	}
	logger.Info("data map length", "dataMap_size", len(dataMap))
	configMap := corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      EsConfigmapName,
			Namespace: esCluster.Namespace,
			Labels: map[string]string{
				"app": "es564",
			},
		},
		Data: dataMap,
	}

	logger.Info("SetControllerReference configMap begin !!!")
	if err := ctrl.SetControllerReference(esCluster, &configMap, r.Scheme); err != nil {
		return ctrl.Result{}, err
	}
	logger.Info("SetControllerReference configMap end !!!")

	if create {
		if err := r.Create(ctx, &configMap); err != nil {
			logger.Error(err, "create ConfigMap err !!!")
			return ctrl.Result{}, err
		}
	} else {
		if err := r.Update(ctx, &configMap); err != nil {
			logger.Error(err, "update ConfigMap err !!!")
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{}, nil
}

func createHeadlessService(ctx context.Context, r *ESClusterReconciler, esCluster *elasticsearchv1alpha1.ESCluster) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	service := corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      EsHeadlessServiceName,
			Namespace: esCluster.Namespace,
			Labels:    map[string]string{"app": "es564"},
		},
		Spec: corev1.ServiceSpec{
			ClusterIP: "None",
			Ports: []corev1.ServicePort{
				{
					Name: "http",
					Port: 9200,
					TargetPort: intstr.IntOrString{
						Type:   intstr.Int,
						IntVal: 9200,
					},
				},
				{
					Name: "transport",
					Port: 9300,
					TargetPort: intstr.IntOrString{
						Type:   intstr.Int,
						IntVal: 9300,
					},
				},
			},
			Selector: map[string]string{
				"app": "es564",
			},
		},
	}

	logger.Info("SetControllerReference service begin !!!")
	if err := ctrl.SetControllerReference(esCluster, &service, r.Scheme); err != nil {
		return ctrl.Result{}, err
	}
	logger.Info("SetControllerReference service end !!!")

	if err := r.Create(ctx, &service); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func createStatefulSet(ctx context.Context, r *ESClusterReconciler, esCluster *elasticsearchv1alpha1.ESCluster, configSum string) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	size := esCluster.Spec.Size
	statefulSet := appv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      EsStatefulSetName,
			Namespace: esCluster.Namespace,
			Labels:    map[string]string{"app": "es564"},
		},
		Spec: appv1.StatefulSetSpec{
			ServiceName: EsHeadlessServiceName,
			Replicas:    &size,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app": "es564",
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app": "es564",
					},
					Annotations: map[string]string{
						EsConfigSumAnnotation: configSum,
					},
				},
				Spec: corev1.PodSpec{
					InitContainers: []corev1.Container{
						{
							Name: "init-elasticsearch",
							Env: []corev1.EnvVar{
								{
									Name:  "ES_JAVA_OPTS",
									Value: "-Xms512m -Xmx512m",
								},
								{
									Name: "POD_INDEX",
									ValueFrom: &corev1.EnvVarSource{
										FieldRef: &corev1.ObjectFieldSelector{
											FieldPath: "metadata.labels['apps.kubernetes.io/pod-index']",
										},
									},
								},
							},
							Image: "busybox",
							Command: []string{
								"sh",
								"-c",
								"echo $POD_INDEX && cp /configs/elasticsearch-$(POD_INDEX).yml /shared-config/elasticsearch.yml && cat /shared-config/elasticsearch.yml",
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "es-configs",
									MountPath: "/configs",
								},
								{
									Name:      "es-shared-config",
									MountPath: "/shared-config",
								},
							},
						},
					},
					Containers: []corev1.Container{
						{
							Name: "elasticsearch",

							Image: "elasticsearch:5.6.4",
							Env: []corev1.EnvVar{
								{
									Name:  "ES_JAVA_OPTS",
									Value: "-Xms512m -Xmx512m",
								},
								{
									Name: "POD_INDEX",
									ValueFrom: &corev1.EnvVarSource{
										FieldRef: &corev1.ObjectFieldSelector{
											FieldPath: "metadata.labels['apps.kubernetes.io/pod-index']",
										},
									},
								},
							},
							Ports: []corev1.ContainerPort{
								{
									Name:          "http",
									ContainerPort: 9200,
								},

								{
									Name:          "transport",
									ContainerPort: 9300,
								},
							},
							ReadinessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									HTTPGet: &corev1.HTTPGetAction{
										Path: "/_cluster/health",
										Port: intstr.FromString("http"),
									},
								},
								InitialDelaySeconds: 10,
								PeriodSeconds:       5,
							},
							LivenessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									HTTPGet: &corev1.HTTPGetAction{
										Path: "/_cluster/health",
										Port: intstr.FromString("http"),
									},
								},
								InitialDelaySeconds: 10,
								PeriodSeconds:       5,
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "es-shared-config",
									MountPath: "/usr/share/elasticsearch/config/elasticsearch.yml",
									SubPath:   "elasticsearch.yml",
								},
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "es-configs",
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: EsConfigmapName,
									},
								},
							},
						},
						{
							Name: "es-shared-config",
							VolumeSource: corev1.VolumeSource{
								EmptyDir: &corev1.EmptyDirVolumeSource{},
							},
						},
					},
				},
			},
		},
	}

	logger.Info("SetControllerReference statefulSet begin !!!")
	if err := ctrl.SetControllerReference(esCluster, &statefulSet, r.Scheme); err != nil {
		return ctrl.Result{}, err
	}
	logger.Info("SetControllerReference statefulSet end !!!")

	if err := r.Create(ctx, &statefulSet); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

type ESConfig struct {
	ClusterName         string   `yaml:"cluster.name"`
	NodeName            string   `yaml:"node.name"`
	NetworkBindHost     string   `yaml:"network.bind_host"`
	NetworkPublishHost  string   `yaml:"network.publish_host"`
	HTTPPort            int      `yaml:"http.port"`
	TransportTCPPort    int      `yaml:"transport.tcp.port"`
	HTTPCORS            bool     `yaml:"http.cors.enabled"`
	HTTPCORSAllowOrigin string   `yaml:"http.cors.allow-origin"`
	NodeMaster          bool     `yaml:"node.master"`
	NodeData            bool     `yaml:"node.data"`
	DiscoveryZenPing    []string `yaml:"discovery.zen.ping.unicast.hosts"`
	DiscoveryZenMin     int      `yaml:"discovery.zen.minimum_master_nodes"`
}

func (config *ESConfig) NewESConfig() ESConfig {
	return ESConfig{
		ClusterName:         "elasticsearch",
		NodeName:            "",
		NetworkBindHost:     "0.0.0.0",
		NetworkPublishHost:  "",
		HTTPPort:            9200,
		TransportTCPPort:    9300,
		HTTPCORS:            true,
		HTTPCORSAllowOrigin: "*",
		NodeMaster:          true,
		NodeData:            true,
		DiscoveryZenPing:    []string{},
		DiscoveryZenMin:     1,
	}
}

func (config *ESConfig) setClusterName(clusterName string) {
	config.ClusterName = clusterName
}

func (config *ESConfig) setNodeName(nodeName string) {
	config.NodeName = nodeName
}

func (config *ESConfig) setNetworkPublishHost(publishHost string) {
	config.NetworkPublishHost = publishHost
}

func (config *ESConfig) setDiscoveryZenPing(discoveryZenPing []string) {
	config.DiscoveryZenPing = discoveryZenPing
}

// SetupWithManager sets up the controller with the Manager.
func (r *ESClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&elasticsearchv1alpha1.ESCluster{}).
		Owns(&appv1.StatefulSet{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.ConfigMap{}).
		Complete(r)
}
