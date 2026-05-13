package kubeconfig

import (
	"encoding/json"

	"gopkg.in/yaml.v3"
)

type Config struct {
	APIVersion     string        `json:"apiVersion"`
	Kind           string        `json:"kind"`
	Clusters       []ClusterInfo `json:"clusters"`
	Contexts       []ContextInfo `json:"contexts"`
	CurrentContext string        `json:"current-context"`
	Preferences    Preferences   `json:"preferences"`
	AuthInfos      []AuthInfo    `json:"users"`
}

type ClusterInfo struct {
	Name    string  `json:"name"`
	Cluster Cluster `json:"cluster"`
}

type Cluster struct {
	CertificateAuthorityData string `json:"certificate-authority-data"`
	Server                   string `json:"server"`
}

type ContextInfo struct {
	Name    string  `json:"name"`
	Context Context `json:"context"`
}

type Context struct {
	Cluster string `json:"cluster"`
	User    string `json:"user"`
}

type AuthInfo struct {
	Name string `json:"name"`
	User User   `json:"user"`
}

type Preferences struct{}

type User struct {
	ClientCertificateData string `json:"client-certificate-data"`
	ClientKeyData         string `json:"client-key-data"`
}

func Parse(yamlBytes string) (*Config, error) {
	var yamlObj interface{}

	err := yaml.Unmarshal([]byte(yamlBytes), &yamlObj)
	if err != nil {
		return nil, err
	}

	jsonBytes, err := json.Marshal(yamlObj)
	if err != nil {
		return nil, err
	}

	var config Config

	err = json.Unmarshal(jsonBytes, &config)
	if err != nil {
		return nil, err
	}

	return &config, nil
}
