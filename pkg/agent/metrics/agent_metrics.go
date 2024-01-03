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

package metrics

import (
	"context"

	"kubefin.dev/kubefin/cmd/kubefin-agent/app/options"
	"kubefin.dev/kubefin/pkg/agent/cloudprice"
	metricscache "kubefin.dev/kubefin/pkg/agent/metrics/cache"
	"kubefin.dev/kubefin/pkg/agent/metrics/core"
	"kubefin.dev/kubefin/pkg/agent/metrics/types"
)

func RegisterAgentMetricsCollector(ctx context.Context,
	options *options.AgentOptions,
	coreResourceInformerLister *options.CoreResourceInformerLister,
	provider cloudprice.CloudProviderInterface,
	metricsClientList *types.MetricsClientList) error {
	usageMetricsCache := metricscache.NewClusterResourceUsageMetricsCache(ctx, options, metricsClientList)
	core.RegisterClusterLevelMetricsCollection(options, provider, coreResourceInformerLister)
	core.RegisterPodLevelMetricsCollection(options, provider, coreResourceInformerLister, usageMetricsCache)
	core.RegisterNodeLevelMetricsCollection(options, provider, coreResourceInformerLister, usageMetricsCache)
	if err := core.RegisterWorkloadLevelMetricsCollection(ctx, options, provider,
		coreResourceInformerLister, usageMetricsCache, metricsClientList); err != nil {
		return err
	}

	return nil
}
