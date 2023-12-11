// Copyright Contributors to the Open Cluster Management project
package join

import (
	"k8s.io/cli-runtime/pkg/genericclioptions"
	clientcmdapiv1 "k8s.io/client-go/tools/clientcmd/api/v1"
	genericclioptionsclusteradm "open-cluster-management.io/clusteradm/pkg/genericclioptions"
	"open-cluster-management.io/clusteradm/pkg/join"
)

// Options: The structure holding all the command-line options
type Options struct {
	//ClusteradmFlags: The generic options from the clusteradm cli-runtime.
	ClusteradmFlags *genericclioptionsclusteradm.ClusteradmFlags

	config join.BootstrapConfig
	//The hub ca-file(optional)
	caFile string
	//the name under the cluster must be imported
	clusterName string

	// OCM agent deploy mode, default to "default".
	mode string
	// managed cluster kubeconfig file, used in hosted mode
	managedKubeconfigFile string
	//Pulling image registry of OCM
	registry string
	// version of predefined compatible image versions
	bundleVersion string

	// if set, deploy the singleton agent rather than klusterlet
	singleton bool

	//The file to output the resources will be sent to the file.
	outputFile string
	//Runs the cluster joining in foreground
	wait bool
	// By default, The installing registration agent will be starting registration using
	// the external endpoint from --hub-apiserver instead of looking for the internal
	// endpoint from the public cluster-info.
	forceHubInClusterEndpointLookup bool
	// By default, the klusterlet running in the hosting cluster will access the managed
	// cluster registered in the hosted mode by using the external endpoint from
	// --managed-cluster-kubeconfig instead of looking for the internal endpoint from the
	// public cluster-info.
	forceManagedInClusterEndpointLookup bool
	hubInClusterEndpoint                string

	//Values below are used to fill in yaml files
	values join.Values

	Streams genericclioptions.IOStreams

	bootKubeConfig clientcmdapiv1.Config
}

func newOptions(clusteradmFlags *genericclioptionsclusteradm.ClusteradmFlags, streams genericclioptions.IOStreams) *Options {
	return &Options{
		ClusteradmFlags: clusteradmFlags,
		Streams:         streams,
		config:          join.BootstrapConfig{},
	}
}
