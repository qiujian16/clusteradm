// Copyright Contributors to the Open Cluster Management project
package join

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"os"
	"reflect"

	"github.com/ghodss/yaml"
	"k8s.io/client-go/kubernetes"
	clientcmdapiv1 "k8s.io/client-go/tools/clientcmd/api/v1"
	certutil "k8s.io/client-go/util/cert"
	"open-cluster-management.io/clusteradm/pkg/helpers"
)

type BootstrapGetter interface {
	KubeConfig() (clientcmdapiv1.Config, error)
	KubeConfigRaw() ([]byte, error)
}

type BootstrapConfig struct {
	Token        string
	CA           []byte
	ProxyURL     string
	ProxyCAFile  string
	HubAPIServer string
}

type TokenBootStrapper struct {
	config BootstrapConfig
	client kubernetes.Interface
}

func NewTokenBootStrapper(config BootstrapConfig, client kubernetes.Interface) BootstrapGetter {
	return &TokenBootStrapper{
		config: config,
		client: client,
	}
}

func (g *TokenBootStrapper) KubeConfigRaw() ([]byte, error) {
	clientConfig, err := g.KubeConfig()
	if err != nil {
		return nil, err
	}

	bootstrapConfigBytes, err := yaml.Marshal(clientConfig)
	if err != nil {
		return nil, err
	}

	return bootstrapConfigBytes, nil
}

func (g *TokenBootStrapper) KubeConfig() (clientcmdapiv1.Config, error) {
	clientConfig := clientcmdapiv1.Config{
		// Define a cluster stanza based on the bootstrap kubeconfig.
		Clusters: []clientcmdapiv1.NamedCluster{
			{
				Name: "hub",
				Cluster: clientcmdapiv1.Cluster{
					Server: g.config.HubAPIServer,
				},
			},
		},
		// Define auth based on the obtained client cert.
		AuthInfos: []clientcmdapiv1.NamedAuthInfo{
			{
				Name: "bootstrap",
				AuthInfo: clientcmdapiv1.AuthInfo{
					Token: g.config.Token,
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

	if g.config.CA != nil {
		// directly set ca-data if --ca-file is set
		clientConfig.Clusters[0].Cluster.CertificateAuthorityData = g.config.CA
	} else {
		// get ca data from, ca may empty(cluster-info exists with no ca data)
		ca, err := helpers.GetCACert(g.client)
		if err != nil {
			return clientConfig, err
		}
		clientConfig.Clusters[0].Cluster.CertificateAuthorityData = ca
	}

	// append the proxy ca data
	if len(g.config.ProxyURL) > 0 && len(g.config.ProxyCAFile) > 0 {
		proxyCAData, err := os.ReadFile(g.config.ProxyCAFile)
		if err != nil {
			return clientConfig, err
		}
		clientConfig.Clusters[0].Cluster.CertificateAuthorityData, err = mergeCertificateData(
			clientConfig.Clusters[0].Cluster.CertificateAuthorityData, proxyCAData)
		if err != nil {
			return clientConfig, err
		}
	}

	return clientConfig, nil
}

func mergeCertificateData(caBundles ...[]byte) ([]byte, error) {
	var all []*x509.Certificate
	for _, caBundle := range caBundles {
		if len(caBundle) == 0 {
			continue
		}
		certs, err := certutil.ParseCertsPEM(caBundle)
		if err != nil {
			return []byte{}, err
		}
		all = append(all, certs...)
	}

	// remove duplicated cert
	var merged []*x509.Certificate
	for i := range all {
		found := false
		for j := range merged {
			if reflect.DeepEqual(all[i].Raw, merged[j].Raw) {
				found = true
				break
			}
		}
		if !found {
			merged = append(merged, all[i])
		}
	}

	// encode the merged certificates
	b := bytes.Buffer{}
	for _, cert := range merged {
		if err := pem.Encode(&b, &pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}); err != nil {
			return []byte{}, err
		}
	}
	return b.Bytes(), nil
}
