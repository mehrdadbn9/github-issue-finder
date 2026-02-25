package main

import (
	"sort"
	"strings"
)

type RepoManager struct {
	included []RepoConfig
	excluded []string
}

type RepoConfig struct {
	Owner    string
	Name     string
	Category string
	Priority int
	MinStars int
	Language string
	Enabled  bool
	Labels   []string
}

var DefaultRepos = []RepoConfig{
	{Owner: "kubernetes-sigs", Name: "kubespray", Category: "kubernetes", Priority: 1, MinStars: 16000, Enabled: true},
	{Owner: "kubernetes", Name: "kubernetes", Category: "kubernetes", Priority: 1, MinStars: 105000, Enabled: true},
	{Owner: "kubernetes-sigs", Name: "kubeadm", Category: "kubernetes", Priority: 2, MinStars: 7000, Enabled: true},
	{Owner: "kubernetes", Name: "minikube", Category: "kubernetes", Priority: 2, MinStars: 29000, Enabled: true},

	{Owner: "cilium", Name: "cilium", Category: "networking", Priority: 1, MinStars: 18000, Enabled: true},
	{Owner: "projectcalico", Name: "calico", Category: "networking", Priority: 2, MinStars: 6000, Enabled: true},
	{Owner: "flannel-io", Name: "flannel", Category: "networking", Priority: 3, MinStars: 9000, Enabled: true},

	{Owner: "rook", Name: "rook", Category: "storage", Priority: 1, MinStars: 12000, Enabled: true},
	{Owner: "openebs", Name: "openebs", Category: "storage", Priority: 2, MinStars: 8000, Enabled: true},
	{Owner: "longhorn", Name: "longhorn", Category: "storage", Priority: 2, MinStars: 5000, Enabled: true},

	{Owner: "VictoriaMetrics", Name: "VictoriaMetrics", Category: "monitoring", Priority: 1, MinStars: 10000, Enabled: true},
	{Owner: "prometheus", Name: "prometheus", Category: "monitoring", Priority: 1, MinStars: 53000, Enabled: true},
	{Owner: "etcd-io", Name: "etcd", Category: "monitoring", Priority: 1, MinStars: 46000, Enabled: true},

	{Owner: "kyverno", Name: "kyverno", Category: "security", Priority: 1, MinStars: 5000, Enabled: true},
	{Owner: "open-policy-agent", Name: "gatekeeper", Category: "security", Priority: 2, MinStars: 3500, Enabled: true},
	{Owner: "aquasecurity", Name: "trivy", Category: "security", Priority: 2, MinStars: 21000, Enabled: true},

	{Owner: "argoproj", Name: "argo-cd", Category: "gitops", Priority: 1, MinStars: 15000, Enabled: true},
	{Owner: "fluxcd", Name: "flux2", Category: "gitops", Priority: 2, MinStars: 6000, Enabled: true},

	{Owner: "hashicorp", Name: "nomad", Category: "infrastructure", Priority: 1, MinStars: 14000, Enabled: true},
	{Owner: "hashicorp", Name: "terraform", Category: "infrastructure", Priority: 1, MinStars: 41000, Enabled: true},

	{Owner: "vmware-tanzu", Name: "velero", Category: "backup", Priority: 1, MinStars: 8000, Enabled: true},
	{Owner: "restic", Name: "restic", Category: "backup", Priority: 2, MinStars: 26000, Enabled: true},

	{Owner: "golang", Name: "go", Category: "go-core", Priority: 1, MinStars: 125000, Enabled: true},
	{Owner: "golang", Name: "crypto", Category: "security", Priority: 1, MinStars: 3000, Enabled: true},
	{Owner: "golang", Name: "net", Category: "networking", Priority: 1, MinStars: 3000, Enabled: true},

	{Owner: "grafana", Name: "grafana", Category: "monitoring", Priority: 1, MinStars: 58000, Enabled: true},
	{Owner: "grafana", Name: "loki", Category: "monitoring", Priority: 2, MinStars: 21000, Enabled: true},

	{Owner: "helm", Name: "helm", Category: "kubernetes", Priority: 1, MinStars: 25000, Enabled: true},
	{Owner: "containerd", Name: "containerd", Category: "kubernetes", Priority: 1, MinStars: 15000, Enabled: true},

	{Owner: "istio", Name: "istio", Category: "networking", Priority: 1, MinStars: 35000, Enabled: true},
	{Owner: "traefik", Name: "traefik", Category: "networking", Priority: 1, MinStars: 50000, Enabled: true},

	{Owner: "hashicorp", Name: "vault", Category: "security", Priority: 1, MinStars: 29000, Enabled: true},
	{Owner: "hashicorp", Name: "consul", Category: "networking", Priority: 2, MinStars: 27000, Enabled: true},

	{Owner: "grpc", Name: "grpc-go", Category: "networking", Priority: 1, MinStars: 21000, Enabled: true},
	{Owner: "dapr", Name: "dapr", Category: "kubernetes", Priority: 1, MinStars: 24000, Enabled: true},

	{Owner: "cert-manager", Name: "cert-manager", Category: "security", Priority: 1, MinStars: 12000, Enabled: true},
	{Owner: "external-secrets", Name: "external-secrets", Category: "security", Priority: 2, MinStars: 4000, Enabled: true},
}

var ExcludedRepos = []string{
	"aws/",
	"GoogleCloudPlatform/",
	"Azure/",
	"awslabs/",
	"googleapis/",
}

func NewRepoManager() *RepoManager {
	rm := &RepoManager{
		included: DefaultRepos,
		excluded: ExcludedRepos,
	}
	return rm
}

func (rm *RepoManager) GetEnabledRepos() []RepoConfig {
	var enabled []RepoConfig
	for _, repo := range rm.included {
		if repo.Enabled {
			enabled = append(enabled, repo)
		}
	}

	sort.Slice(enabled, func(i, j int) bool {
		return enabled[i].Priority < enabled[j].Priority
	})

	return enabled
}

func (rm *RepoManager) GetRepo(owner, name string) *RepoConfig {
	for _, repo := range rm.included {
		if repo.Owner == owner && repo.Name == name {
			return &repo
		}
	}
	return nil
}

func (rm *RepoManager) AddRepo(repo RepoConfig) {
	for i, existing := range rm.included {
		if existing.Owner == repo.Owner && existing.Name == repo.Name {
			rm.included[i] = repo
			return
		}
	}
	rm.included = append(rm.included, repo)
}

func (rm *RepoManager) RemoveRepo(owner, name string) bool {
	for i, repo := range rm.included {
		if repo.Owner == owner && repo.Name == name {
			rm.included = append(rm.included[:i], rm.included[i+1:]...)
			return true
		}
	}
	return false
}

func (rm *RepoManager) IsExcluded(owner, name string) bool {
	repoFullName := owner + "/" + name
	for _, excluded := range rm.excluded {
		if strings.HasPrefix(repoFullName, excluded) {
			return true
		}
		if excluded == repoFullName {
			return true
		}
	}
	return false
}

func (rm *RepoManager) ListRepos() []RepoConfig {
	return rm.included
}

func (rm *RepoManager) ListByCategory(category string) []RepoConfig {
	var result []RepoConfig
	for _, repo := range rm.included {
		if strings.EqualFold(repo.Category, category) {
			result = append(result, repo)
		}
	}
	return result
}

func (rm *RepoManager) GetCategories() []string {
	categories := make(map[string]bool)
	for _, repo := range rm.included {
		categories[repo.Category] = true
	}

	var result []string
	for cat := range categories {
		result = append(result, cat)
	}
	sort.Strings(result)
	return result
}
