// Copyright Contributors to the Open Cluster Management project
package join

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/ghodss/yaml"
	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapiv1 "k8s.io/client-go/tools/clientcmd/api/v1"
	"k8s.io/klog/v2"
	"k8s.io/kubectl/pkg/cmd/util"
	operatorclient "open-cluster-management.io/api/client/operator/clientset/versioned"
	ocmfeature "open-cluster-management.io/api/feature"
	operatorv1 "open-cluster-management.io/api/operator/v1"
	"open-cluster-management.io/clusteradm/pkg/cmd/join/preflight"
	genericclioptionsclusteradm "open-cluster-management.io/clusteradm/pkg/genericclioptions"
	"open-cluster-management.io/clusteradm/pkg/helpers"
	preflightinterface "open-cluster-management.io/clusteradm/pkg/helpers/preflight"
	"open-cluster-management.io/clusteradm/pkg/helpers/printer"
	"open-cluster-management.io/clusteradm/pkg/helpers/version"
	"open-cluster-management.io/clusteradm/pkg/join"
)

const (
	AgentNamespacePrefix = "open-cluster-management-"

	OperatorNamesapce   = "open-cluster-management"
	DefaultOperatorName = "klusterlet"
)

func format(s string) string {
	if s == "" {
		return ""
	}
	return strings.ToUpper(s[:1]) + strings.ToLower(s[1:])
}

func (o *Options) complete(cmd *cobra.Command, args []string) (err error) {
	if cmd.Flags() == nil {
		return fmt.Errorf("no flags have been set: hub-apiserver, hub-token and cluster-name is required")
	}

	if o.config.Token == "" {
		return fmt.Errorf("token is missing")
	}
	if o.config.HubAPIServer == "" {
		return fmt.Errorf("hub-server is missing")
	}
	if o.clusterName == "" {
		return fmt.Errorf("cluster-name is missing")
	}
	if len(o.registry) == 0 {
		return fmt.Errorf("the OCM image registry should not be empty, like quay.io/open-cluster-management")
	}

	if len(o.mode) == 0 {
		return fmt.Errorf("the mode should not be empty, like default")
	}
	// convert mode string to lower
	o.mode = format(o.mode)

	klog.V(1).InfoS("join options:", "dry-run", o.ClusteradmFlags.DryRun, "cluster", o.clusterName, "api-server", o.config.HubAPIServer, "output", o.outputFile)

	agentNamespace := AgentNamespacePrefix + "agent"

	o.values = join.Values{
		ClusterName: o.clusterName,
		Hub: join.Hub{
			APIServer: o.config.HubAPIServer,
		},
		Registry:       o.registry,
		AgentNamespace: agentNamespace,
	}
	// deploy klusterlet
	// operatorNamespace is the namespace to deploy klsuterlet;
	// agentNamespace is the namesapce to deploy the agents(registration agent, work agent, etc.);
	// klusterletNamespace is the namespace created on the managed cluster for each klusterlet.
	//
	// The operatorNamespace is fixed to "open-cluster-management".
	// In default mode, agentNamespace is "open-cluster-management-agent", klusterletNamespace refers to agentNamespace, all of these three namesapces are on the managed cluster;
	// In hosted mode, operatorNamespace is on the management cluster, agentNamesapce is "<cluster name>-<6-bit random string>" on the management cluster, and the klusterletNamespace is "open-cluster-management-<agentNamespace>" on the managed cluster.

	// values for default mode
	klusterletName := DefaultOperatorName
	if o.mode == string(operatorv1.InstallModeHosted) {
		// add hash suffix to avoid conflict
		klusterletName += "-hosted-" + helpers.RandStringRunes_az09(6)
		// update AgentNamespace
		o.values.AgentNamespace = klusterletName
	}

	o.values.Klusterlet = join.Klusterlet{
		Name: klusterletName,
	}
	o.values.ManagedKubeconfig = o.managedKubeconfigFile
	o.values.RegistrationFeatures = genericclioptionsclusteradm.ConvertToFeatureGateAPI(genericclioptionsclusteradm.SpokeMutableFeatureGate, ocmfeature.DefaultSpokeRegistrationFeatureGates)
	o.values.WorkFeatures = genericclioptionsclusteradm.ConvertToFeatureGateAPI(genericclioptionsclusteradm.SpokeMutableFeatureGate, ocmfeature.DefaultSpokeWorkFeatureGates)

	// set mode based on mode and singleton
	if o.mode == string(operatorv1.InstallModeHosted) && o.singleton {
		o.values.Klusterlet.Mode = string(operatorv1.InstallModeSingletonHosted)
	} else if o.singleton {
		o.values.Klusterlet.Mode = string(operatorv1.InstallModeSingleton)
	} else {
		o.values.Klusterlet.Mode = o.mode
	}

	versionBundle, err := version.GetVersionBundle(o.bundleVersion)

	if err != nil {
		klog.Errorf("unable to retrieve version ", err)
		return err
	}

	o.values.BundleVersion = join.BundleVersion{
		RegistrationImageVersion: versionBundle.Registration,
		PlacementImageVersion:    versionBundle.Placement,
		WorkImageVersion:         versionBundle.Work,
		OperatorImageVersion:     versionBundle.Operator,
	}
	klog.V(3).InfoS("Image version:",
		"'registration image version'", versionBundle.Registration,
		"'placement image version'", versionBundle.Placement,
		"'work image version'", versionBundle.Work,
		"'operator image version'", versionBundle.Operator,
		"'singleton agent image version'", versionBundle.Operator)

	// if --ca-file is set, read ca data
	if o.caFile != "" {
		cabytes, err := os.ReadFile(o.caFile)
		if err != nil {
			return err
		}
		o.config.CA = cabytes
	}

	// code logic of building hub client in join process:
	// 1. use the token and insecure to fetch the ca data from cm in kube-public ns
	// 2. if not found, assume using an authorized ca.
	// 3. use the ca and token to build a secured client and call hub

	//Create an unsecure bootstrap
	bootstrapExternalConfigUnSecure := o.createExternalBootstrapConfig()
	//create external client from the bootstrap
	externalClientUnSecure, err := helpers.CreateClientFromClientcmdapiv1Config(bootstrapExternalConfigUnSecure)
	if err != nil {
		return err
	}

	bootGetter := join.NewTokenBootStrapper(o.config, externalClientUnSecure)
	bootKubeConfigRaw, err := bootGetter.KubeConfigRaw()
	if err != nil {
		return err
	}
	bootKubeConfig, err := bootGetter.KubeConfig()
	if err != nil {
		return err
	}
	o.bootKubeConfig = bootKubeConfig
	o.values.Hub.KubeConfig = base64.StdEncoding.EncodeToString(bootKubeConfigRaw)

	// get managed cluster externalServerURL
	var kubeClient *kubernetes.Clientset
	switch o.mode {
	case string(operatorv1.InstallModeHosted):
		restConfig, err := clientcmd.BuildConfigFromFlags("", o.managedKubeconfigFile)
		if err != nil {
			return err
		}
		kubeClient, err = kubernetes.NewForConfig(restConfig)
		if err != nil {
			return err
		}
	default:
		kubeClient, err = o.ClusteradmFlags.KubectlFactory.KubernetesClientSet()
		if err != nil {
			klog.Errorf("Failed building kube client: %v", err)
			return err
		}
	}

	klusterletApiserver, err := helpers.GetAPIServer(kubeClient)
	if err != nil {
		klog.Warningf("Failed looking for cluster endpoint for the registering klusterlet: %v", err)
		klusterletApiserver = ""
	} else if !preflight.ValidAPIHost(klusterletApiserver) {
		klog.Warningf("ConfigMap/cluster-info.data.kubeconfig.clusters[0].cluster.server field [%s] in namespace kube-public should start with http:// or https://", klusterletApiserver)
		klusterletApiserver = ""
	}
	o.values.Klusterlet.APIServer = klusterletApiserver

	klog.V(3).InfoS("values:",
		"clusterName", o.values.ClusterName,
		"hubAPIServer", o.values.Hub.APIServer,
		"klusterletAPIServer", o.values.Klusterlet.APIServer)
	return nil

}

func (o *Options) validate() error {
	// preflight check
	if err := preflightinterface.RunChecks(
		[]preflightinterface.Checker{
			preflight.HubKubeconfigCheck{
				Config: &o.bootKubeConfig,
			},
			preflight.DeployModeCheck{
				Mode:                  o.mode,
				InternalEndpoint:      o.forceHubInClusterEndpointLookup,
				ManagedKubeconfigFile: o.managedKubeconfigFile,
			},
			preflight.ClusterNameCheck{
				ClusterName: o.values.ClusterName,
			},
		}, os.Stderr); err != nil {
		return err
	}

	// get ManagedKubeconfig from given file
	if o.mode == string(operatorv1.InstallModeHosted) {
		managedConfig, err := os.ReadFile(o.managedKubeconfigFile)
		if err != nil {
			return err
		}

		// replace the server address with the internal endpoint
		if o.forceManagedInClusterEndpointLookup {
			config := &clientcmdapiv1.Config{}
			err = yaml.Unmarshal(managedConfig, config)
			if err != nil {
				return err
			}
			restConfig, err := clientcmd.BuildConfigFromFlags("", o.managedKubeconfigFile)
			if err != nil {
				return err
			}
			kubeClient, err := kubernetes.NewForConfig(restConfig)
			if err != nil {
				return err
			}
			inClusterEndpoint, err := helpers.GetAPIServer(kubeClient)
			if err != nil {
				return err
			}
			config.Clusters[0].Cluster.Server = inClusterEndpoint
			managedConfig, err = yaml.Marshal(config)
			if err != nil {
				return err
			}
		}
		o.values.ManagedKubeconfig = base64.StdEncoding.EncodeToString(managedConfig)
	}

	return nil
}

func (o *Options) run() error {
	b := join.NewBuilder().
		WithSpokeKubeConfig(o.ClusteradmFlags.KubectlFactory.ToRawKubeConfigLoader()).
		WithValues(o.values)

	applied, err := b.ApplyImport(context.TODO(), o.ClusteradmFlags.DryRun, o.wait, true, o.Streams)
	if err != nil {
		return err
	}

	config, err := o.ClusteradmFlags.KubectlFactory.ToRESTConfig()
	if err != nil {
		return err
	}

	operatorClient, err := operatorclient.NewForConfig(config)
	if err != nil {
		return err
	}

	if o.wait && !o.ClusteradmFlags.DryRun {
		err = waitUntilRegistrationOperatorConditionIsTrue(o.ClusteradmFlags.KubectlFactory, int64(o.ClusteradmFlags.Timeout))
		if err != nil {
			return err
		}
		if o.mode == string(operatorv1.InstallModeHosted) {
			err = waitUntilKlusterletConditionIsTrue(operatorClient, int64(o.ClusteradmFlags.Timeout), o.values.Klusterlet.Name)
			if err != nil {
				return err
			}
		} else {
			err = waitUntilKlusterletConditionIsTrue(operatorClient, int64(o.ClusteradmFlags.Timeout), o.values.Klusterlet.Name)
			if err != nil {
				return err
			}
		}
	}

	if len(o.outputFile) > 0 {
		sh, err := os.OpenFile(o.outputFile, os.O_CREATE|os.O_WRONLY, 0755)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(sh, "%s", string(applied))
		if err != nil {
			return err
		}
		if err := sh.Close(); err != nil {
			return err
		}
	}

	fmt.Fprintf(o.Streams.Out, "Please log onto the hub cluster and run the following command:\n\n"+
		"    %s accept --clusters %s\n\n", helpers.GetExampleHeader(), o.values.ClusterName)
	return nil

}

func checkIfRegistrationOperatorAvailable(f util.Factory) (bool, error) {
	var restConfig *rest.Config
	restConfig, err := f.ToRESTConfig()
	if err != nil {
		return false, err
	}
	client, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return false, err
	}

	deploy, err := client.AppsV1().Deployments(OperatorNamesapce).
		Get(context.TODO(), DefaultOperatorName, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}

	conds := make([]metav1.Condition, len(deploy.Status.Conditions))
	for i := range deploy.Status.Conditions {
		conds[i] = metav1.Condition{
			Type:    string(deploy.Status.Conditions[i].Type),
			Status:  metav1.ConditionStatus(deploy.Status.Conditions[i].Status),
			Reason:  deploy.Status.Conditions[i].Reason,
			Message: deploy.Status.Conditions[i].Message,
		}
	}
	return meta.IsStatusConditionTrue(conds, "Available"), nil
}

func waitUntilRegistrationOperatorConditionIsTrue(f util.Factory, timeout int64) error {
	var restConfig *rest.Config
	restConfig, err := f.ToRESTConfig()
	if err != nil {
		return err
	}
	client, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return err
	}

	phase := &atomic.Value{}
	phase.Store("")
	operatorSpinner := printer.NewSpinnerWithStatus(
		"Waiting for registration operator to become ready...",
		time.Millisecond*500,
		"Registration operator is now available.\n",
		func() string {
			return phase.Load().(string)
		})
	operatorSpinner.Start()
	defer operatorSpinner.Stop()

	return helpers.WatchUntil(
		func() (watch.Interface, error) {
			return client.CoreV1().Pods(OperatorNamesapce).
				Watch(context.TODO(), metav1.ListOptions{
					TimeoutSeconds: &timeout,
					LabelSelector:  "app=klusterlet",
				})
		},
		func(event watch.Event) bool {
			pod, ok := event.Object.(*corev1.Pod)
			if !ok {
				return false
			}
			phase.Store(printer.GetSpinnerPodStatus(pod))
			conds := make([]metav1.Condition, len(pod.Status.Conditions))
			for i := range pod.Status.Conditions {
				conds[i] = metav1.Condition{
					Type:    string(pod.Status.Conditions[i].Type),
					Status:  metav1.ConditionStatus(pod.Status.Conditions[i].Status),
					Reason:  pod.Status.Conditions[i].Reason,
					Message: pod.Status.Conditions[i].Message,
				}
			}
			return meta.IsStatusConditionTrue(conds, "Ready")
		})
}

// Wait until the klusterlet condition available=true, or timeout in $timeout seconds
func waitUntilKlusterletConditionIsTrue(client operatorclient.Interface, timeout int64, klusterletName string) error {
	phase := &atomic.Value{}
	phase.Store("")
	klusterletSpinner := printer.NewSpinnerWithStatus(
		"Waiting for klusterlet agent to become ready...",
		time.Millisecond*500,
		"Klusterlet is now available.\n",
		func() string {
			return phase.Load().(string)
		})
	klusterletSpinner.Start()
	defer klusterletSpinner.Stop()

	return helpers.WatchUntil(
		func() (watch.Interface, error) {
			return client.OperatorV1().Klusterlets().
				Watch(context.TODO(), metav1.ListOptions{
					TimeoutSeconds: &timeout,
					FieldSelector:  fmt.Sprintf("metadata.name=%s", klusterletName),
				})
		},
		func(event watch.Event) bool {
			klusterlet, ok := event.Object.(*operatorv1.Klusterlet)
			if !ok {
				return false
			}
			phase.Store(printer.GetSpinnerKlusterletStatus(klusterlet))
			return meta.IsStatusConditionFalse(klusterlet.Status.Conditions, "RegistrationDesiredDegraded") &&
				meta.IsStatusConditionFalse(klusterlet.Status.Conditions, "WorkDesiredDegraded")
		},
	)
}

// Create bootstrap with token but without CA
func (o *Options) createExternalBootstrapConfig() clientcmdapiv1.Config {
	return clientcmdapiv1.Config{
		// Define a cluster stanza based on the bootstrap kubeconfig.
		Clusters: []clientcmdapiv1.NamedCluster{
			{
				Name: "hub",
				Cluster: clientcmdapiv1.Cluster{
					Server:                o.config.HubAPIServer,
					InsecureSkipTLSVerify: true,
				},
			},
		},
		// Define auth based on the obtained client cert.
		AuthInfos: []clientcmdapiv1.NamedAuthInfo{
			{
				Name: "bootstrap",
				AuthInfo: clientcmdapiv1.AuthInfo{
					Token: o.config.Token,
				},
			},
		},
		// Define a context that connects the auth info and cluster, and set it as the default
		Contexts: []clientcmdapiv1.NamedContext{
			{
				Name: "bootstrap",
				Context: clientcmdapiv1.Context{
					Cluster:   "hub",
					AuthInfo:  "bootstrap",
					Namespace: "default",
				},
			},
		},
		CurrentContext: "bootstrap",
	}
}
