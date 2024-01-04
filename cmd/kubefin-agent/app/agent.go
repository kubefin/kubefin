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

package app

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/spf13/cobra"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	cliflag "k8s.io/component-base/cli/flag"
	"k8s.io/klog/v2"
	metricsv "k8s.io/metrics/pkg/client/clientset/versioned"

	"kubefin.dev/kubefin/cmd/kubefin-agent/app/options"
	"kubefin.dev/kubefin/pkg/agent/cloudprice"
	"kubefin.dev/kubefin/pkg/agent/metrics"
	"kubefin.dev/kubefin/pkg/agent/metrics/types"
	kubefinclient "kubefin.dev/kubefin/pkg/generated/clientset/versioned"
	kubefininformer "kubefin.dev/kubefin/pkg/generated/informers/externalversions"
)

// NewAgentCommand creates a *cobra.Command object with defaultcloud parameters
func NewAgentCommand(ctx context.Context) *cobra.Command {
	opts := options.NewAgentOptions()

	cmd := &cobra.Command{
		Use:  "kubefin-agent",
		Long: `kubefin-agent used to scrap metrics to storage store such as thanos`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := opts.Complete(); err != nil {
				return err
			}
			if err := opts.Validate(); err != nil {
				return err
			}

			if err := Run(ctx, opts); err != nil {
				return err
			}

			return nil
		},
	}

	fss := cliflag.NamedFlagSets{}

	agentFlagSet := fss.FlagSet("agent")
	opts.AddFlags(agentFlagSet)

	logFlagSet := fss.FlagSet("log")
	klog.InitFlags(flag.CommandLine)
	logFlagSet.AddGoFlagSet(flag.CommandLine)

	cmd.Flags().AddFlagSet(agentFlagSet)
	cmd.Flags().AddFlagSet(logFlagSet)

	return cmd
}

func Run(ctx context.Context, opts *options.AgentOptions) error {
	klog.Infof("Start kubefin-agent...")
	config, err := rest.InClusterConfig()
	if err != nil {
		return fmt.Errorf("find values for connect kube-apiserver error:%v", err)
	}
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("create client to connect kube-apiserver error:%v", err)
	}
	kubefinClient, err := kubefinclient.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("create kubefin client to connect kube-apiserver error:%v", err)
	}
	dynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		klog.Fatalf("create dynamic client to connect kube-apiserver error:%v", err)
	}
	metricsClient, err := metricsv.NewForConfig(config)
	if err != nil {
		klog.Fatalf("create metrics client to connect kube-apiserver error:%v", err)
	}
	discoveryClient, err := discovery.NewDiscoveryClientForConfig(config)
	if err != nil {
		klog.Fatalf("create discovery client to connect kube-apiserver error:%v", err)
	}
	metricsClientList := &types.MetricsClientList{
		MetricsClient:   metricsClient,
		DiscoveryClient: discoveryClient,
		DynamicClient:   dynamicClient,
	}

	server := &http.Server{
		Addr: ":8080",
	}
	runFunc := func(runCtx context.Context) {
		// TODO: Support pull metrics from API, and ensure the requests will go to the right pod
		provider, err := cloudprice.NewCloudProvider(client, opts)
		if err != nil {
			klog.Fatalf("Create cloud provider error:%v", err)
		}
		provider.Start(runCtx)

		k8sFactory := informers.NewSharedInformerFactory(client, 0)
		kubefinFactory := kubefininformer.NewSharedInformerFactory(kubefinClient, 0)
		coreResourceInformerLister := getAllCoreResourceLister(k8sFactory, kubefinFactory)
		if err := metrics.RegisterAgentMetricsCollector(ctx, opts, coreResourceInformerLister,
			provider, metricsClientList); err != nil {
			klog.Fatalf("Register agent metrics collector error:%v", err)
		}

		stopCh := ctx.Done()
		k8sFactory.Start(stopCh)
		kubefinFactory.Start(stopCh)

		klog.Infof("Wait resource cache sync...")
		if ok := cache.WaitForCacheSync(stopCh,
			coreResourceInformerLister.NodeInformer.HasSynced,
			coreResourceInformerLister.NamespaceInformer.HasSynced,
			coreResourceInformerLister.PodInformer.HasSynced,
			coreResourceInformerLister.DeploymentInformer.HasSynced,
			coreResourceInformerLister.StatefulSetInformer.HasSynced,
			coreResourceInformerLister.DaemonSetInformer.HasSynced,
			coreResourceInformerLister.CustomWorkloadCfgInformer.HasSynced); !ok {
			klog.Fatalf("wait core resource cache sync failed")
		}

		mux := http.NewServeMux()
		mux.Handle("/metrics", promhttp.Handler())
		server.Handler = mux
		klog.Infof("Start metrics http server")
		go func() {
			if err := server.ListenAndServe(); err != nil {
				klog.Fatalf("Start http server error:%v", err)
			}
		}()
	}
	stopFun := func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		server.Shutdown(ctx)
	}
	if err := runLeaderElection(ctx, client, opts, runFunc, stopFun); err != nil {
		return fmt.Errorf("run leader election error:%v", err)
	}

	return nil
}

func runLeaderElection(ctx context.Context, clientset kubernetes.Interface,
	opts *options.AgentOptions, runFunc func(ctx context.Context), stopFunc func()) error {
	rl, err := resourcelock.New(opts.LeaderElection.ResourceLock,
		opts.LeaderElection.ResourceNamespace,
		opts.LeaderElection.ResourceName,
		clientset.CoreV1(),
		clientset.CoordinationV1(),
		resourcelock.ResourceLockConfig{Identity: opts.LeaderElectionID})
	if err != nil {
		return fmt.Errorf("couldn't create resource lock: %v", err)
	}
	leaderElectionCfg := leaderelection.LeaderElectionConfig{
		Lock:          rl,
		LeaseDuration: opts.LeaderElection.LeaseDuration.Duration,
		RenewDeadline: opts.LeaderElection.RenewDeadline.Duration,
		RetryPeriod:   opts.LeaderElection.RetryPeriod.Duration,
		WatchDog:      leaderelection.NewLeaderHealthzAdaptor(time.Second * 20),
		Name:          opts.LeaderElection.ResourceName,
	}
	leaderElectionCfg.Callbacks = leaderelection.LeaderCallbacks{
		OnStartedLeading: runFunc,
		OnStoppedLeading: stopFunc,
	}
	leaderElector, err := leaderelection.NewLeaderElector(leaderElectionCfg)
	if err != nil {
		return fmt.Errorf("couldn't create leader elector:%v", err)
	}
	leaderElector.Run(ctx)
	return nil
}

func getAllCoreResourceLister(k8sFactory informers.SharedInformerFactory,
	kubfinFactory kubefininformer.SharedInformerFactory) *options.CoreResourceInformerLister {
	coreResource := k8sFactory.Core().V1()
	appsResource := k8sFactory.Apps().V1()
	kubefinResource := kubfinFactory.Insight().V1alpha1()
	return &options.CoreResourceInformerLister{
		NodeInformer:              coreResource.Nodes().Informer(),
		NamespaceInformer:         coreResource.Namespaces().Informer(),
		PodInformer:               coreResource.Pods().Informer(),
		DeploymentInformer:        appsResource.Deployments().Informer(),
		StatefulSetInformer:       appsResource.StatefulSets().Informer(),
		DaemonSetInformer:         appsResource.DaemonSets().Informer(),
		CustomWorkloadCfgInformer: kubefinResource.CustomAllocationConfigurations().Informer(),
		NodeLister:                coreResource.Nodes().Lister(),
		PodLister:                 coreResource.Pods().Lister(),
		DeploymentLister:          appsResource.Deployments().Lister(),
		StatefulSetLister:         appsResource.StatefulSets().Lister(),
		DaemonSetLister:           appsResource.DaemonSets().Lister(),
		CustomWorkloadCfgLister:   kubefinResource.CustomAllocationConfigurations().Lister(),
	}
}
