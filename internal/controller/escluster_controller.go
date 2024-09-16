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
	"fmt"
	"gopkg.in/yaml.v3"
	appv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
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

//+kubebuilder:rbac:groups=elasticsearch.yasy.run,resources=esclusters,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=elasticsearch.yasy.run,resources=esclusters/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=elasticsearch.yasy.run,resources=esclusters/finalizers,verbs=update
//+kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=core,resources=configmaps,verbs=get;list;watch;create;update;patch;delete

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
	esCluster := &elasticsearchv1alpha1.ESCluster{}

	// if the ESCluster crd not exists, return and do nothing
	if err := r.Get(ctx, req.NamespacedName, esCluster); err != nil {
		logger.Error(err, "unable to fetch ESCluster")
		return ctrl.Result{}, err
	}
	// get esCluster size and name
	clusterSize := esCluster.Spec.Size
	clusterName := esCluster.Spec.ClusterName

	logger.Info("esCluster info:", "clusterSize", clusterSize, "clusterName", clusterName)

	// get the existing pods,services and configMap

	configMapList := corev1.ConfigMapList{}
	// find the configMap  has a label  app=es564
	if err := r.List(ctx, &configMapList, client.MatchingLabels{"app": "es564"}); err != nil {
		logger.Error(err, "unable to list ConfigMap")
		return ctrl.Result{}, err
	}

	if len(configMapList.Items) == 0 {
		_, err := createEsConfigMap(ctx, r, esCluster)
		if err != nil {
			return ctrl.Result{}, err
		}
	} else {
		logger.Info("un support update configMap now !!!")
	}
	// find the headless service that is owned by the esCluster
	serviceList := corev1.ServiceList{}
	err := r.List(ctx, &serviceList, client.InNamespace(esCluster.Namespace), client.MatchingLabels{"app": "es564"})
	if err != nil {
		logger.Error(err, "fetch elasticsearch headless service err !!!")
		return ctrl.Result{}, err
	}
	if len(serviceList.Items) == 0 {
		if _, err := createHeadlessService(ctx, r, esCluster); err != nil {
			logger.Error(err, "create elasticsearch headless service err !!!")
			return ctrl.Result{}, err
		}
	}

	// find the pods that are owned by the esCluster
	statefulSetList := appv1.StatefulSetList{}
	if err = r.List(ctx, &statefulSetList, client.InNamespace(esCluster.Namespace), client.MatchingLabels{"app": "es564"}); err != nil {
		logger.Error(err, "fetch elasticsearch statefulList err !!!")
		return ctrl.Result{}, err
	}

	if len(statefulSetList.Items) == 0 {
		if _, err = createStatefulSet(ctx, r, esCluster); err != nil {
			logger.Error(err, "create elasticsearch statefulList err !!!")
			return ctrl.Result{}, err
		}
	}

	logger.Info("End reconciling EsCluster....")

	return ctrl.Result{}, nil
}

func createEsConfigMap(ctx context.Context, r *ESClusterReconciler, cluster *elasticsearchv1alpha1.ESCluster) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	clusterName := cluster.Spec.ClusterName
	size := cluster.Spec.Size
	var discoveryZenPing []string
	dataMap := make(map[string]string)
	for i := 0; i < int(size); i++ {
		dataMapKey := fmt.Sprintf("elasticsearch-%d.yml", i)
		esConfig := ESConfig{}
		config := esConfig.NewESConfig()
		config.setClusterName(clusterName)
		config.setNodeName(fmt.Sprintf("elasticsearch-%d.elasticsearch-headless.%s.svc.cluster.local", i, cluster.Namespace))
		config.setNetworkPublishHost(fmt.Sprintf("elasticsearch-%d.elasticsearch-headless.%s.svc.cluster.local", i, cluster.Namespace))
		discoveryZenPing = append(discoveryZenPing, fmt.Sprintf("elasticsearch-%d.elasticsearch-headless.%s.svc.cluster.local:9300", i, cluster.Namespace))
		config.setDiscoveryZenPing(discoveryZenPing)
		marshal, err := yaml.Marshal(config)
		if err != nil {
			return ctrl.Result{}, err
		}
		dataMap[dataMapKey] = string(marshal)
	}
	logger.Info("data map length %d", "dataMap_size", len(dataMap))
	configMap := corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-configmap", cluster.Spec.ClusterName),
			Namespace: cluster.Namespace,
			Labels: map[string]string{
				"app": "es564",
			},
		},
		Data: dataMap,
	}
	err := r.Create(ctx, &configMap)
	if err != nil {
		logger.Error(err, "create ConfigMap err !!!")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func createHeadlessService(ctx context.Context, r *ESClusterReconciler, cluster *elasticsearchv1alpha1.ESCluster) (ctrl.Result, error) {
	service := corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "elasticsearch-headless",
			Namespace: cluster.Namespace,
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
	err := r.Create(ctx, &service)
	if err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func createStatefulSet(ctx context.Context, r *ESClusterReconciler, esCluster *elasticsearchv1alpha1.ESCluster) (ctrl.Result, error) {
	size := esCluster.Spec.Size
	statefulSet := appv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "elasticsearch",
			Namespace: esCluster.Namespace,
			Labels:    map[string]string{"app": "es564"},
		},
		Spec: appv1.StatefulSetSpec{
			ServiceName: "elasticsearch-headless",
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
							Name:  "elasticsearch",
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
										Name: fmt.Sprintf("%s-configmap", esCluster.Spec.ClusterName),
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

	err := r.Create(ctx, &statefulSet)
	if err != nil {
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
		Complete(r)
}
