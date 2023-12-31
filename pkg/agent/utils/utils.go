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

package utils

import (
	"github.com/prometheus/client_golang/prometheus"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	listercorev1 "k8s.io/client-go/listers/core/v1"
	"k8s.io/klog/v2"

	"kubefin.dev/kubefin/pkg/agent/cloudprice"
	"kubefin.dev/kubefin/pkg/values"
)

func ConvertPrometheusLabelValuesInOrder(keyOrder []string, labels prometheus.Labels) []string {
	ret := []string{}
	for _, key := range keyOrder {
		ret = append(ret, labels[key])
	}
	return ret
}

func ConvertQualityToCore(value *resource.Quantity) float64 {
	return value.AsApproximateFloat64()
}

func ConvertQualityToGiB(value *resource.Quantity) float64 {
	return value.AsApproximateFloat64() / values.GBInBytes
}

func ParsePodResourceCost(pod *v1.Pod, provider cloudprice.CloudProviderInterface, lister listercorev1.NodeLister) float64 {
	cpu, ram, gpu := ParsePodResourceRequest(pod, pod.Spec.NodeName != "")

	var cpuTotal float64
	for _, value := range cpu {
		cpuTotal += value
	}

	var ramTotal float64
	for _, value := range ram {
		ramTotal += value
	}

	var gpuTotal float64
	for _, value := range gpu {
		gpuTotal += value
	}

	node, err := lister.Get(pod.Spec.NodeName)
	if err != nil {
		klog.Errorf("failed to get node %s: %v", pod.Spec.NodeName, err)
		return 0
	}
	priceInfo, err := provider.GetNodeHourlyPrice(node)
	if err != nil {
		klog.Errorf("failed to get node %s: %v", pod.Spec.NodeName, err)
		return 0
	}

	cpuCosts := cpuTotal * priceInfo.CPUCoreHourlyPrice
	memoryCosts := ramTotal * priceInfo.RAMGiBHourlyPrice
	gpuCosts := gpuTotal * priceInfo.GPUCardHourlyPrice
	return cpuCosts + memoryCosts + gpuCosts
}

func ParsePodResourceRequest(pod *v1.Pod, scheduled bool) (cpu, ram, gpu map[string]float64) {
	cpu = make(map[string]float64)
	ram = make(map[string]float64)
	gpu = make(map[string]float64)
	// Referring issue: https://github.com/kubefin/kubefin/issues/28
	if pod.Status.Phase == v1.PodSucceeded || pod.Status.Phase == v1.PodFailed || !scheduled {
		for _, container := range pod.Spec.Containers {
			cpu[container.Name] = 0.0
			ram[container.Name] = 0.0
		}
		return
	}
	for _, container := range pod.Spec.Containers {
		if _, ok := cpu[container.Name]; !ok {
			cpu[container.Name] = 0.0
		}
		if _, ok := ram[container.Name]; !ok {
			ram[container.Name] = 0.0
		}
		if _, ok := gpu[container.Name]; !ok {
			gpu[container.Name] = 0.0
		}
		cpu[container.Name] += float64(container.Resources.Requests.Cpu().MilliValue()) / values.CoreInMCore
		ram[container.Name] += float64(container.Resources.Requests.Memory().Value()) / values.GBInBytes
		gpu[container.Name] += float64(container.Resources.Requests.Name(values.ResourceGPU, resource.DecimalSI).Value())
	}
	return
}
