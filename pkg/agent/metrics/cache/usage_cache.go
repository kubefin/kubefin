/*
Copyright 2023 The KubeFin Authors

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

package cache

import (
	"context"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	"k8s.io/metrics/pkg/client/clientset/versioned"

	"kubefin.dev/kubefin/cmd/kubefin-agent/app/options"
	"kubefin.dev/kubefin/pkg/agent/metrics/types"
	"kubefin.dev/kubefin/pkg/agent/utils"
)

type ResourceUsageMetric struct {
	ResourceName string
	CPUUsage     float64
	MemoryUsage  float64
}

type PodUsageMetric struct {
	Name            string
	Namespace       string
	ContainersUsage []ResourceUsageMetric
}

type ClusterResourceUsageMetricsCache struct {
	ctx           context.Context
	metricsClient *versioned.Clientset
	options       *options.AgentOptions

	podMutex  sync.RWMutex
	podsUsage []PodUsageMetric

	nodeMutex  sync.RWMutex
	nodesUsage []ResourceUsageMetric
}

func NewClusterResourceUsageMetricsCache(ctx context.Context,
	agentOptions *options.AgentOptions,
	metricsClientList *types.MetricsClientList) *ClusterResourceUsageMetricsCache {
	usageMetricsCache := &ClusterResourceUsageMetricsCache{
		ctx:           ctx,
		metricsClient: metricsClientList.MetricsClient,
		options:       agentOptions,
		podMutex:      sync.RWMutex{},
		podsUsage:     []PodUsageMetric{},
		nodeMutex:     sync.RWMutex{},
		nodesUsage:    []ResourceUsageMetric{},
	}
	go usageMetricsCache.Start()

	return usageMetricsCache
}

func (c *ClusterResourceUsageMetricsCache) Start() {
	ticker := time.NewTicker(c.options.ScrapMetricsInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			c.CollectNodeMetrics()
			c.CollectPodMetrics()
		case <-c.ctx.Done():
			return
		}
	}
}

func (c *ClusterResourceUsageMetricsCache) CollectPodMetrics() {
	pods, err := c.metricsClient.MetricsV1beta1().PodMetricses(corev1.NamespaceAll).
		List(c.ctx, metav1.ListOptions{})
	if err != nil {
		klog.Errorf("Get pods metrics error:%v", err)
		return
	}

	c.podMutex.Lock()
	defer c.podMutex.Unlock()

	data := []PodUsageMetric{}
	for _, pod := range pods.Items {
		podUsage := PodUsageMetric{
			Name:            pod.Name,
			Namespace:       pod.Namespace,
			ContainersUsage: []ResourceUsageMetric{},
		}
		for _, container := range pod.Containers {
			podUsage.ContainersUsage = append(podUsage.ContainersUsage, ResourceUsageMetric{
				ResourceName: container.Name,
				CPUUsage:     utils.ConvertQualityToCore(container.Usage.Cpu()),
				MemoryUsage:  utils.ConvertQualityToGiB(container.Usage.Memory()),
			})
		}
		data = append(data, podUsage)
	}
	c.podsUsage = data
}

func (c *ClusterResourceUsageMetricsCache) CollectNodeMetrics() {
	nodes, err := c.metricsClient.MetricsV1beta1().NodeMetricses().List(c.ctx, metav1.ListOptions{})
	if err != nil {
		klog.Errorf("Get nodes metrics error:%v", err)
		return
	}

	c.nodeMutex.Lock()
	defer c.nodeMutex.Unlock()

	data := []ResourceUsageMetric{}
	for _, node := range nodes.Items {
		data = append(data, ResourceUsageMetric{
			ResourceName: node.Name,
			CPUUsage:     utils.ConvertQualityToCore(node.Usage.Cpu()),
			MemoryUsage:  utils.ConvertQualityToGiB(node.Usage.Memory()),
		})
	}
	c.nodesUsage = data
}

func (c *ClusterResourceUsageMetricsCache) QueryWorkloadsUsageByPods(pods ...*corev1.Pod) []PodUsageMetric {
	c.podMutex.RLock()
	defer c.podMutex.RUnlock()

	ret := []PodUsageMetric{}
	for _, pod := range pods {
		// TODO: Can we not use the loop?
		for _, podUsage := range c.podsUsage {
			if pod.Namespace == podUsage.Namespace && pod.Name == podUsage.Name {
				ret = append(ret, podUsage)
				break
			}
		}
	}

	return ret
}

func (c *ClusterResourceUsageMetricsCache) QueryAllPodsUsage() []PodUsageMetric {
	c.podMutex.RLock()
	defer c.podMutex.RUnlock()

	return append([]PodUsageMetric{}, c.podsUsage...)
}

func (c *ClusterResourceUsageMetricsCache) QueryAllNodesUsage() []ResourceUsageMetric {
	c.nodeMutex.RLock()
	defer c.nodeMutex.RUnlock()

	return append([]ResourceUsageMetric{}, c.nodesUsage...)
}
