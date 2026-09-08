package kube

import (
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"
)

// restClientGetter adapts an existing rest.Config to the RESTClientGetter
// interface that the Helm SDK expects when initialising an action.
type restClientGetter struct {
	namespace string
	config    *rest.Config
}

func newRESTClientGetter(cfg *rest.Config, namespace string) *restClientGetter {
	return &restClientGetter{namespace: namespace, config: cfg}
}

func (g *restClientGetter) ToRESTConfig() (*rest.Config, error) {
	return g.config, nil
}

func (g *restClientGetter) ToDiscoveryClient() (discovery.CachedDiscoveryInterface, error) {
	dc, err := discovery.NewDiscoveryClientForConfig(g.config)
	if err != nil {
		return nil, err
	}
	return memory.NewMemCacheClient(dc), nil
}

func (g *restClientGetter) ToRESTMapper() (meta.RESTMapper, error) {
	dc, err := g.ToDiscoveryClient()
	if err != nil {
		return nil, err
	}
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(dc)
	return restmapper.NewShortcutExpander(mapper, dc, nil), nil
}

func (g *restClientGetter) ToRawKubeConfigLoader() clientcmd.ClientConfig {
	// Helm mostly wants this for the namespace.
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	overrides := &clientcmd.ConfigOverrides{}
	overrides.Context.Namespace = g.namespace
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, overrides)
}
