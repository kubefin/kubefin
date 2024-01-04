/*
Copyright 2022 The KubeFin Authors

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

package core

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	listersappv1 "k8s.io/client-go/listers/apps/v1"
	listercorev1 "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/restmapper"
	"k8s.io/klog/v2"

	"kubefin.dev/kubefin/cmd/kubefin-agent/app/options"
	"kubefin.dev/kubefin/pkg/agent/cloudprice"
	metricscache "kubefin.dev/kubefin/pkg/agent/metrics/cache"
	"kubefin.dev/kubefin/pkg/agent/metrics/types"
	"kubefin.dev/kubefin/pkg/agent/reference"
	"kubefin.dev/kubefin/pkg/agent/utils"
	insightv1alpha1 "kubefin.dev/kubefin/pkg/apis/insight/v1alpha1"
	insightlister "kubefin.dev/kubefin/pkg/generated/listers/insight/v1alpha1"
	"kubefin.dev/kubefin/pkg/values"
)

var (
	workloadCRNoneCareLabelKey = []string{
		values.WorkloadTypeLabelKey,
		values.WorkloadNameLabelKey,
		values.NamespaceLabelKey,
		values.ClusterNameLabelKey,
		values.ClusterIdLabelKey,
		values.LabelsLabelKey,
		values.ResourceTypeLabelKey,
	}
	workloadResourceCostDesc = prometheus.NewDesc(
		values.WorkloadResourceCostMetricsName,
		"The workload resource cost", workloadCRNoneCareLabelKey, nil)
	workloadPodCountDesc = prometheus.NewDesc(
		values.WorkloadPodCountMetricsName,
		"The workload pod count", workloadCRNoneCareLabelKey, nil)

	workloadCRCareLabelKey = []string{
		values.WorkloadTypeLabelKey,
		values.WorkloadNameLabelKey,
		values.NamespaceLabelKey,
		values.ClusterNameLabelKey,
		values.ClusterIdLabelKey,
		values.LabelsLabelKey,
		values.ResourceTypeLabelKey,
		// For multiple container workload, this metrics is needed for cpu/memory size recommendation
		values.ContainerNameLabelKey,
	}
	workloadResourceRequestDesc = prometheus.NewDesc(
		values.WorkloadResourceRequestMetricsName,
		"The workload resource request", workloadCRCareLabelKey, nil)
	workloadResourceUsageDesc = prometheus.NewDesc(
		values.WorkloadResourceUsageMetricsName,
		"The workload resource usage", workloadCRCareLabelKey, nil)
)

type workloadMetricsCollector struct {
	clusterName string
	clusterId   string

	discoveryClient   *discovery.DiscoveryClient
	dynamicClient     dynamic.Interface
	usageMetricsCache *metricscache.ClusterResourceUsageMetricsCache
	provider          cloudprice.CloudProviderInterface

	mapperMutex    sync.RWMutex
	resourceMapper meta.RESTMapper

	podLister               listercorev1.PodLister
	nodeLister              listercorev1.NodeLister
	daemonSetLister         listersappv1.DaemonSetLister
	statefulSetLister       listersappv1.StatefulSetLister
	deploymentLister        listersappv1.DeploymentLister
	customWorkloadCfgLister insightlister.CustomAllocationConfigurationLister
}

func (w *workloadMetricsCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- workloadResourceCostDesc
	ch <- workloadPodCountDesc
	ch <- workloadResourceRequestDesc
	ch <- workloadResourceUsageDesc
}

func (w *workloadMetricsCollector) Collect(ch chan<- prometheus.Metric) {
	w.collectDeploymentResourceMetrics(ch)
	w.collectDaemonSetResourceMetrics(ch)
	w.collectStatefulSetResourceMetrics(ch)
	w.collectCustomWorkloadMetrics(ch)
}

func (w *workloadMetricsCollector) collectDeploymentResourceMetrics(ch chan<- prometheus.Metric) {
	deployments, err := w.deploymentLister.List(labels.Everything())
	if err != nil {
		klog.Errorf("List all deployment error:%v", err)
		return
	}
	for _, deployment := range deployments {
		deploymentLabels, err := json.Marshal(deployment.Labels)
		if err != nil {
			klog.Errorf("Marshal daemonSet labels error:%v", err)
			continue
		}
		selector, err := metav1.LabelSelectorAsSelector(deployment.Spec.Selector)
		if err != nil {
			klog.Errorf("Parse deployment selector error:%v", err)
			return
		}

		containerNoneCareLabelValues := prometheus.Labels{
			values.WorkloadTypeLabelKey: "deployment",
			values.WorkloadNameLabelKey: deployment.Name,
			values.NamespaceLabelKey:    deployment.Namespace,
			values.ClusterNameLabelKey:  w.clusterName,
			values.ClusterIdLabelKey:    w.clusterId,
			values.LabelsLabelKey:       string(deploymentLabels),
		}
		pods, err := w.podLister.List(selector)
		if err != nil {
			klog.Errorf("List pods error:%v", err)
			continue
		}
		w.collectPodMetrics(pods, containerNoneCareLabelValues, ch)
		w.collectCostMetrics(pods, containerNoneCareLabelValues, ch)

		containerCareLabelValues := prometheus.Labels{
			values.WorkloadTypeLabelKey: "deployment",
			values.WorkloadNameLabelKey: deployment.Name,
			values.NamespaceLabelKey:    deployment.Namespace,
			values.ClusterNameLabelKey:  w.clusterName,
			values.ClusterIdLabelKey:    w.clusterId,
			values.LabelsLabelKey:       string(deploymentLabels),
		}
		w.collectResourceRequestMetrics(pods, containerCareLabelValues, ch)
		w.collectResourceUsageMetrics(pods, containerCareLabelValues, ch)
	}
}

func (w *workloadMetricsCollector) collectDaemonSetResourceMetrics(ch chan<- prometheus.Metric) {
	daemonSets, err := w.daemonSetLister.List(labels.Everything())
	if err != nil {
		klog.Errorf("List all daemonSets error:%v", err)
		return
	}
	for _, daemonSet := range daemonSets {
		daemonSetLabels, err := json.Marshal(daemonSet.Labels)
		if err != nil {
			klog.Errorf("Marshal daemonSet labels error:%v", err)
			continue
		}
		selector, err := metav1.LabelSelectorAsSelector(daemonSet.Spec.Selector)
		if err != nil {
			klog.Errorf("Parse deployment selector error:%v", err)
			return
		}

		containerNoneCareLabelValues := prometheus.Labels{
			values.WorkloadTypeLabelKey: "daemonset",
			values.WorkloadNameLabelKey: daemonSet.Name,
			values.NamespaceLabelKey:    daemonSet.Namespace,
			values.ClusterNameLabelKey:  w.clusterName,
			values.ClusterIdLabelKey:    w.clusterId,
			values.LabelsLabelKey:       string(daemonSetLabels),
		}
		pods, err := w.podLister.List(selector)
		if err != nil {
			klog.Errorf("List pods error:%v", err)
			continue
		}
		w.collectPodMetrics(pods, containerNoneCareLabelValues, ch)
		w.collectCostMetrics(pods, containerNoneCareLabelValues, ch)

		containerCareLabelValues := prometheus.Labels{
			values.WorkloadTypeLabelKey: "daemonset",
			values.WorkloadNameLabelKey: daemonSet.Name,
			values.NamespaceLabelKey:    daemonSet.Namespace,
			values.ClusterNameLabelKey:  w.clusterName,
			values.ClusterIdLabelKey:    w.clusterId,
			values.LabelsLabelKey:       string(daemonSetLabels),
		}
		w.collectResourceRequestMetrics(pods, containerCareLabelValues, ch)
		w.collectResourceUsageMetrics(pods, containerCareLabelValues, ch)
	}
}

func (w *workloadMetricsCollector) collectStatefulSetResourceMetrics(ch chan<- prometheus.Metric) {
	statefulSets, err := w.statefulSetLister.List(labels.Everything())
	if err != nil {
		klog.Errorf("List all statefulSet error:%v", err)
		return
	}
	for _, statefulSet := range statefulSets {
		statefulSetLabels, err := json.Marshal(statefulSet.Labels)
		if err != nil {
			klog.Errorf("Marshal daemonSet labels error:%v", err)
			continue
		}
		selector, err := metav1.LabelSelectorAsSelector(statefulSet.Spec.Selector)
		if err != nil {
			klog.Errorf("Parse deployment selector error:%v", err)
			return
		}

		containerNoneCareLabelValues := prometheus.Labels{
			values.WorkloadTypeLabelKey: "statefulset",
			values.WorkloadNameLabelKey: statefulSet.Name,
			values.NamespaceLabelKey:    statefulSet.Namespace,
			values.ClusterNameLabelKey:  w.clusterName,
			values.ClusterIdLabelKey:    w.clusterId,
			values.LabelsLabelKey:       string(statefulSetLabels),
		}
		pods, err := w.podLister.List(selector)
		if err != nil {
			klog.Errorf("List pods error:%v", err)
			continue
		}
		w.collectPodMetrics(pods, containerNoneCareLabelValues, ch)
		w.collectCostMetrics(pods, containerNoneCareLabelValues, ch)

		containerCareLabelValues := prometheus.Labels{
			values.WorkloadTypeLabelKey: "statefulset",
			values.WorkloadNameLabelKey: statefulSet.Name,
			values.NamespaceLabelKey:    statefulSet.Namespace,
			values.ClusterNameLabelKey:  w.clusterName,
			values.ClusterIdLabelKey:    w.clusterId,
			values.LabelsLabelKey:       string(statefulSetLabels),
		}
		w.collectResourceRequestMetrics(pods, containerCareLabelValues, ch)
		w.collectResourceUsageMetrics(pods, containerCareLabelValues, ch)
	}
}

func (w *workloadMetricsCollector) collectPodMetrics(pods []*corev1.Pod, labels prometheus.Labels, ch chan<- prometheus.Metric) {
	podCount := float64(len(pods))
	labels[values.ResourceTypeLabelKey] = "pod"
	ch <- prometheus.MustNewConstMetric(workloadPodCountDesc,
		prometheus.GaugeValue, podCount, utils.ConvertPrometheusLabelValuesInOrder(workloadCRNoneCareLabelKey, labels)...)
}

func (w *workloadMetricsCollector) collectResourceUsageMetrics(pods []*corev1.Pod, labels prometheus.Labels, ch chan<- prometheus.Metric) {
	podsUsage := w.usageMetricsCache.QueryWorkloadsUsageByPods(pods...)
	cpuTotalUsage, memoryTotalUsage := map[string]float64{}, map[string]float64{}
	for _, pod := range podsUsage {
		for _, ctrInfo := range pod.ContainersUsage {
			if _, ok := cpuTotalUsage[ctrInfo.ResourceName]; !ok {
				cpuTotalUsage[ctrInfo.ResourceName] = 0
			}
			if _, ok := memoryTotalUsage[ctrInfo.ResourceName]; !ok {
				memoryTotalUsage[ctrInfo.ResourceName] = 0
			}
			cpuTotalUsage[ctrInfo.ResourceName] += ctrInfo.CPUUsage
			memoryTotalUsage[ctrInfo.ResourceName] += ctrInfo.MemoryUsage
		}
	}

	labels[values.ResourceTypeLabelKey] = string(corev1.ResourceCPU)
	for containerName, cpu := range cpuTotalUsage {
		labels[values.ContainerNameLabelKey] = containerName
		ch <- prometheus.MustNewConstMetric(workloadResourceUsageDesc,
			prometheus.GaugeValue, cpu, utils.ConvertPrometheusLabelValuesInOrder(workloadCRCareLabelKey, labels)...)
	}

	labels[values.ResourceTypeLabelKey] = string(corev1.ResourceMemory)
	for containerName, ram := range memoryTotalUsage {
		labels[values.ContainerNameLabelKey] = containerName
		ch <- prometheus.MustNewConstMetric(workloadResourceUsageDesc,
			prometheus.GaugeValue, ram, utils.ConvertPrometheusLabelValuesInOrder(workloadCRCareLabelKey, labels)...)
	}
}

func (w *workloadMetricsCollector) collectCostMetrics(pods []*corev1.Pod, labels prometheus.Labels, ch chan<- prometheus.Metric) {
	totalCost := 0.0
	for _, pod := range pods {
		totalCost += utils.ParsePodResourceCost(pod, w.provider, w.nodeLister)
	}

	labels[values.ResourceTypeLabelKey] = "cost"
	ch <- prometheus.MustNewConstMetric(workloadResourceCostDesc,
		prometheus.GaugeValue, totalCost, utils.ConvertPrometheusLabelValuesInOrder(workloadCRNoneCareLabelKey, labels)...)
}

func (c *workloadMetricsCollector) collectCustomWorkloadMetrics(ch chan<- prometheus.Metric) {
	customWorkloadConfigurations, err := c.customWorkloadCfgLister.List(labels.Everything())
	if err != nil {
		klog.Errorf("List custom workload configurations failed: %v", err)
		return
	}

	for _, groupWorkloads := range customWorkloadConfigurations {
		for _, workloadCfg := range groupWorkloads.Spec.WorkloadsAllocation {
			gvr, err := c.parseCustomTargetToGVR(workloadCfg.Target)
			if err != nil {
				continue
			}
			workloads, err := c.dynamicClient.Resource(gvr).List(context.Background(), metav1.ListOptions{})
			if err != nil {
				klog.Errorf("List custom workload(%v) failed: %v", workloadCfg.Target, err)
				continue
			}
			script := workloadCfg.PodLabelSelectorExtract.Script
			for _, w := range workloads.Items {
				podLabelSelector, err := reference.ExtractPodLabelSelector(&w, script)
				if err != nil {
					klog.Errorf("Extract pod label selector failed: %v", err)
					continue
				}
				pods, err := c.podLister.Pods(w.GetNamespace()).List(labels.SelectorFromSet(podLabelSelector))
				if err != nil {
					klog.Errorf("List pods failed: %v", err)
					continue
				}

				workLabels, err := json.Marshal(w.GetLabels())
				if err != nil {
					klog.Errorf("Marshal workload labels error:%v", err)
					continue
				}
				containerNoneCareLabelValues := prometheus.Labels{
					values.WorkloadTypeLabelKey: workloadCfg.WorkloadTypeAlias,
					values.WorkloadNameLabelKey: w.GetName(),
					values.NamespaceLabelKey:    w.GetNamespace(),
					values.ClusterNameLabelKey:  c.clusterName,
					values.ClusterIdLabelKey:    c.clusterId,
					values.LabelsLabelKey:       string(workLabels),
				}
				c.collectPodMetrics(pods, containerNoneCareLabelValues, ch)
				c.collectCostMetrics(pods, containerNoneCareLabelValues, ch)

				containerCareLabelValues := prometheus.Labels{
					values.WorkloadTypeLabelKey: workloadCfg.WorkloadTypeAlias,
					values.WorkloadNameLabelKey: w.GetName(),
					values.NamespaceLabelKey:    w.GetNamespace(),
					values.ClusterNameLabelKey:  c.clusterName,
					values.ClusterIdLabelKey:    c.clusterId,
					values.LabelsLabelKey:       string(workLabels),
				}
				c.collectResourceRequestMetrics(pods, containerCareLabelValues, ch)
				c.collectResourceUsageMetrics(pods, containerCareLabelValues, ch)
			}
		}
	}
}

func (c *workloadMetricsCollector) parseCustomTargetToGVR(target insightv1alpha1.WorkloadAllocationTarget) (schema.GroupVersionResource, error) {
	gv, err := schema.ParseGroupVersion(target.APIVersion)
	if err != nil {
		klog.V(4).Infof("Error parsing GroupVersion: %v", err)
		return schema.GroupVersionResource{}, err
	}
	gk := schema.GroupKind{Group: gv.Group, Kind: target.Kind}

	c.mapperMutex.RLock()
	defer c.mapperMutex.RUnlock()
	gvr, err := c.resourceMapper.RESTMapping(gk, gv.Version)
	if err != nil {
		klog.V(6).Infof("Error getting GVR: %s", err.Error())
		return schema.GroupVersionResource{}, err
	}
	return gvr.Resource, nil
}

func (c *workloadMetricsCollector) collectResourceRequestMetrics(pods []*corev1.Pod, labels prometheus.Labels, ch chan<- prometheus.Metric) {
	cpuTotalRequest, ramTotalRequest, gpuTotalRequest := map[string]float64{}, map[string]float64{}, map[string]float64{}
	for _, pod := range pods {
		cpu, ram, gpu := utils.ParsePodResourceRequest(pod, pod.Spec.NodeName != "")
		for containerName, value := range cpu {
			if _, ok := cpuTotalRequest[containerName]; !ok {
				cpuTotalRequest[containerName] = 0
			}
			cpuTotalRequest[containerName] += value
		}
		for containerName, value := range ram {
			if _, ok := ramTotalRequest[containerName]; !ok {
				ramTotalRequest[containerName] = 0
			}
			ramTotalRequest[containerName] += value
		}
		for containerName, value := range gpu {
			if _, ok := gpuTotalRequest[containerName]; !ok {
				gpuTotalRequest[containerName] = 0
			}
			gpuTotalRequest[containerName] += value
		}
	}

	labels[values.ResourceTypeLabelKey] = string(corev1.ResourceCPU)
	for containerName, cpu := range cpuTotalRequest {
		labels[values.ContainerNameLabelKey] = containerName
		ch <- prometheus.MustNewConstMetric(workloadResourceRequestDesc,
			prometheus.GaugeValue, cpu, utils.ConvertPrometheusLabelValuesInOrder(workloadCRCareLabelKey, labels)...)
	}
	labels[values.ResourceTypeLabelKey] = string(corev1.ResourceMemory)
	for containerName, ram := range ramTotalRequest {
		labels[values.ContainerNameLabelKey] = containerName
		ch <- prometheus.MustNewConstMetric(workloadResourceRequestDesc,
			prometheus.GaugeValue, ram, utils.ConvertPrometheusLabelValuesInOrder(workloadCRCareLabelKey, labels)...)
	}
	labels[values.ResourceTypeLabelKey] = string(values.ResourceGPU)
	for containerName, gpu := range gpuTotalRequest {
		labels[values.ContainerNameLabelKey] = containerName
		ch <- prometheus.MustNewConstMetric(workloadResourceRequestDesc,
			prometheus.GaugeValue, gpu, utils.ConvertPrometheusLabelValuesInOrder(workloadCRCareLabelKey, labels)...)
	}
}

func (c *workloadMetricsCollector) getClusterResourceMapper() (meta.RESTMapper, error) {
	groupResources, err := restmapper.GetAPIGroupResources(c.discoveryClient)
	if err != nil {
		klog.V(4).Infof("Get api group resources error:%v", err)
		return nil, err
	}
	mapper := restmapper.NewDiscoveryRESTMapper(groupResources)
	return mapper, nil
}

func (c *workloadMetricsCollector) refreshResourceMapper(ctx context.Context) error {
	resourceMapper, err := c.getClusterResourceMapper()
	if err != nil {
		return err
	}
	c.resourceMapper = resourceMapper

	ticker := time.NewTicker(time.Minute * 3)
	defer ticker.Stop()
	// The API resources may change during the lifetime of the agent, so we need to refresh the resource mapper periodically.
	// Like the users install a new CRD, or the CRD is deleted.
	go func() {
		for {
			select {
			case <-ticker.C:
				resourceMapper, err := c.getClusterResourceMapper()
				if err != nil {
					klog.Errorf("Get cluster resource mapper error:%v", err)
					continue
				}

				c.mapperMutex.Lock()
				c.resourceMapper = resourceMapper
				c.mapperMutex.Unlock()
			case <-ctx.Done():
				return
			}
		}
	}()

	return nil
}

func RegisterWorkloadLevelMetricsCollection(ctx context.Context,
	agentOptions *options.AgentOptions,
	provider cloudprice.CloudProviderInterface,
	coreResourceInformerLister *options.CoreResourceInformerLister,
	usageMetricsCache *metricscache.ClusterResourceUsageMetricsCache,
	metricsClientList *types.MetricsClientList) error {
	workloadMetricsCollector := &workloadMetricsCollector{
		clusterId:               agentOptions.ClusterId,
		clusterName:             agentOptions.ClusterName,
		dynamicClient:           metricsClientList.DynamicClient,
		discoveryClient:         metricsClientList.DiscoveryClient,
		usageMetricsCache:       usageMetricsCache,
		provider:                provider,
		podLister:               coreResourceInformerLister.PodLister,
		nodeLister:              coreResourceInformerLister.NodeLister,
		deploymentLister:        coreResourceInformerLister.DeploymentLister,
		statefulSetLister:       coreResourceInformerLister.StatefulSetLister,
		daemonSetLister:         coreResourceInformerLister.DaemonSetLister,
		customWorkloadCfgLister: coreResourceInformerLister.CustomWorkloadCfgLister,
	}
	if err := workloadMetricsCollector.refreshResourceMapper(ctx); err != nil {
		return err
	}

	prometheus.MustRegister(workloadMetricsCollector)
	return nil
}
