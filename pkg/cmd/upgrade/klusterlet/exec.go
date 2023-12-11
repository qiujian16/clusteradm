// Copyright Contributors to the Open Cluster Management project
package klusterlet

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	operatorclient "open-cluster-management.io/api/client/operator/clientset/versioned"
	"open-cluster-management.io/clusteradm/pkg/helpers"
	"open-cluster-management.io/clusteradm/pkg/helpers/version"
	"open-cluster-management.io/clusteradm/pkg/join"
)

//nolint:deadcode,varcheck
const (
	klusterletName = "klusterlet"
)

func (o *Options) complete(cmd *cobra.Command, args []string) (err error) {
	err = o.ClusteradmFlags.ValidateManagedCluster()
	if err != nil {
		return err
	}

	f := o.ClusteradmFlags.KubectlFactory
	o.builder = f.NewBuilder()

	cfg, err := f.ToRESTConfig()
	if err != nil {
		return err
	}

	operatorClient, err := operatorclient.NewForConfig(cfg)
	if err != nil {
		return err
	}

	k, err := operatorClient.OperatorV1().Klusterlets().Get(context.TODO(), klusterletName, metav1.GetOptions{})
	if err != nil {
		return err
	}

	versionBundle, err := version.GetVersionBundle(o.bundleVersion)
	if err != nil {
		klog.Errorf("unable to retrieve version ", err)
		return err
	}

	klog.V(1).InfoS("init options:", "dry-run", o.ClusteradmFlags.DryRun)
	o.values = join.Values{
		Registry:    o.registry,
		ClusterName: k.Spec.ClusterName,
		Klusterlet: join.Klusterlet{
			Name: k.Name,
			Mode: string(k.Spec.DeployOption.Mode),
		},
		BundleVersion: join.BundleVersion{
			RegistrationImageVersion: versionBundle.Registration,
			PlacementImageVersion:    versionBundle.Placement,
			WorkImageVersion:         versionBundle.Work,
			OperatorImageVersion:     versionBundle.Operator,
		},
	}

	// reconstruct values from the klusterlet CR.
	if k.Spec.RegistrationConfiguration != nil {
		o.values.RegistrationFeatures = k.Spec.RegistrationConfiguration.FeatureGates
	}
	if k.Spec.WorkConfiguration != nil {
		o.values.WorkFeatures = k.Spec.WorkConfiguration.FeatureGates
	}

	return nil
}

func (o *Options) validate() error {

	restConfig, err := o.ClusteradmFlags.KubectlFactory.ToRESTConfig()
	if err != nil {
		return err
	}
	apiExtensionsClient, err := apiextensionsclient.NewForConfig(restConfig)
	if err != nil {
		return err
	}
	installed, err := helpers.IsKlusterletsInstalled(apiExtensionsClient)
	if err != nil {
		return err
	}

	if !installed {
		return fmt.Errorf("klusterlet is not installed")
	}
	fmt.Fprint(o.Streams.Out, "Klusterlet installed. starting upgrade ")
	fmt.Fprint(o.Streams.Out, "Klusterlet installed. starting upgrade\n")

	return nil
}

func (o *Options) run() error {
	b := join.NewBuilder().
		WithSpokeKubeConfig(o.ClusteradmFlags.KubectlFactory.ToRawKubeConfigLoader()).
		WithValues(o.values)

	_, err := b.ApplyImport(context.TODO(), o.ClusteradmFlags.DryRun, o.wait, false, o.Streams)
	if err != nil {
		return err
	}

	fmt.Fprint(o.Streams.Out, "upgraded completed successfully\n")

	return nil
}
