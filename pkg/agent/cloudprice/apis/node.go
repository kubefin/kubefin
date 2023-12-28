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

package apis

const (
	DefaultCPUMemoryCostRatio = 3.0
	DefaultGPUCPUCostRatio    = 16.0
)

type NodeSpec struct {
	// CPUCount represents the total cpu cores of the node
	CPUCount float64
	// RAMGiBCount represents the total memory GiB of the node
	RAMGiBCount float64
	// GPUCount represents the total gpu cores of the node
	GPUAmount float64
	// Price represents the hourly price of the node, such as 3$/hour
	Price float64
}
