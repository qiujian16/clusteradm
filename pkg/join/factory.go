// Copyright Contributors to the Open Cluster Management project
package join

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"
	cmdutil "k8s.io/kubectl/pkg/cmd/util"
	operatorv1 "open-cluster-management.io/api/operator/v1"
	"open-cluster-management.io/clusteradm/pkg/helpers"
	"open-cluster-management.io/clusteradm/pkg/helpers/reader"
	"open-cluster-management.io/clusteradm/pkg/helpers/wait"
	"open-cluster-management.io/clusteradm/pkg/join/scenario"
)

type Builder struct {
	values          Values
	spokeKubeConfig clientcmd.ClientConfig
}

// Values: The values used in the template
type Values struct {
	//ClusterName: the name of the joined cluster on the hub
	ClusterName string
	//AgentNamespace: the namespace to deploy the agent
	AgentNamespace string
	//Hub: Hub information
	Hub Hub
	//Klusterlet is the klusterlet related configuration
	Klusterlet Klusterlet
	//Registry is the image registry related configuration
	Registry string
	//bundle version
	BundleVersion BundleVersion
	// managed kubeconfig
	ManagedKubeconfig string

	// Features is the slice of feature for registration
	RegistrationFeatures []operatorv1.FeatureGate

	// Features is the slice of feature for work
	WorkFeatures []operatorv1.FeatureGate
}

// Hub: The hub values for the template
type Hub struct {
	//APIServer: The API Server external URL
	APIServer string
	//KubeConfig: The kubeconfig of the bootstrap secret to connect to the hub
	KubeConfig string
}

// Klusterlet is for templating klusterlet configuration
type Klusterlet struct {
	//APIServer: The API Server external URL
	APIServer string
	Mode      string
	Name      string
}

type BundleVersion struct {
	// registration image version
	RegistrationImageVersion string
	// placement image version
	PlacementImageVersion string
	// work image version
	WorkImageVersion string
	// operator image version
	OperatorImageVersion string
}

func NewBuilder() *Builder {
	return &Builder{}
}

func (b *Builder) WithValues(v Values) *Builder {
	b.values = v
	return b
}

func (b *Builder) WithSpokeKubeConfig(config clientcmd.ClientConfig) *Builder {
	b.spokeKubeConfig = config
	return b
}

func (b *Builder) ApplyImport(ctx context.Context, dryRun, shouldWait, bootstrap bool, streams genericclioptions.IOStreams) ([]byte, error) {
	f := cmdutil.NewFactory(b)
	kubeClient, apiExtensionsClient, _, err := helpers.GetClients(f)
	if err != nil {
		return nil, err
	}
	r := reader.NewResourceReader(f.NewBuilder(), dryRun, streams)

	_, err = kubeClient.CoreV1().Namespaces().Get(ctx, b.values.AgentNamespace, metav1.GetOptions{})
	if errors.IsNotFound(err) {
		_, err = kubeClient.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: b.values.AgentNamespace,
				Annotations: map[string]string{
					"workload.openshift.io/allowed": "management",
				},
			},
		}, metav1.CreateOptions{})
		if err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}

	var files []string
	files = append(files,
		"join/klusterlets.crd.yaml",
		"join/namespace.yaml",
		"join/service_account.yaml",
		"join/cluster_role.yaml",
		"join/cluster_role_binding.yaml",
	)

	if bootstrap {
		files = append(files, "bootstrap_hub_kubeconfig.yaml")
	}

	if b.values.Klusterlet.Name == string(operatorv1.InstallModeHosted) {
		files = append(files,
			"join/hosted/external_managed_kubeconfig.yaml",
		)
	}

	err = r.Apply(scenario.Files, b.values, files...)
	if err != nil {
		return r.RawAppliedResources(), err
	}

	if !dryRun {
		if err := wait.WaitUntilCRDReady(apiExtensionsClient, "klusterlets.operator.open-cluster-management.io", shouldWait); err != nil {
			return r.RawAppliedResources(), err
		}
	}

	err = r.Apply(scenario.Files, b.values, "join/klusterlets.cr.yaml")
	if err != nil {
		return r.RawAppliedResources(), err
	}

	return r.RawAppliedResources(), nil
}

func (b *Builder) ToRESTConfig() (*rest.Config, error) {
	return b.spokeKubeConfig.ClientConfig()
}

func (b *Builder) ToDiscoveryClient() (discovery.CachedDiscoveryInterface, error) {
	restconfig, err := b.spokeKubeConfig.ClientConfig()
	if err != nil {
		return nil, err
	}
	dc, err := discovery.NewDiscoveryClientForConfig(restconfig)
	if err != nil {
		return nil, err
	}
	return memory.NewMemCacheClient(dc), nil
}

func (b *Builder) ToRESTMapper() (meta.RESTMapper, error) {
	dc, err := b.ToDiscoveryClient()
	if err != nil {
		return nil, err
	}
	return restmapper.NewDeferredDiscoveryRESTMapper(dc), nil
}

func (b *Builder) ToRawKubeConfigLoader() clientcmd.ClientConfig {
	return b.spokeKubeConfig
}
