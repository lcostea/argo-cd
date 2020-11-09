package cluster

import (
	"context"
	"testing"
	"time"

	"github.com/argoproj/gitops-engine/pkg/utils/kube/kubetest"
	"github.com/dgrijalva/jwt-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/utils/pointer"

	"github.com/argoproj/argo-cd/common"
	clusterapi "github.com/argoproj/argo-cd/pkg/apiclient/cluster"
	"github.com/argoproj/argo-cd/pkg/apis/application/v1alpha1"
	appv1 "github.com/argoproj/argo-cd/pkg/apis/application/v1alpha1"
	servercache "github.com/argoproj/argo-cd/server/cache"
	"github.com/argoproj/argo-cd/server/rbacpolicy"
	"github.com/argoproj/argo-cd/test"
	cacheutil "github.com/argoproj/argo-cd/util/cache"
	appstatecache "github.com/argoproj/argo-cd/util/cache/appstate"
	dbmocks "github.com/argoproj/argo-cd/util/db/mocks"
	"github.com/argoproj/argo-cd/util/rbac"
)

func newServerInMemoryCache() *servercache.Cache {
	return servercache.NewCache(
		appstatecache.NewCache(
			cacheutil.NewCache(cacheutil.NewInMemoryCache(1*time.Hour)),
			1*time.Minute,
		),
		1*time.Minute,
		1*time.Minute,
		1*time.Minute,
	)
}

func newNoopEnforcer() *rbac.Enforcer {
	enf := rbac.NewEnforcer(fake.NewSimpleClientset(test.NewFakeConfigMap()), test.FakeArgoCDNamespace, common.ArgoCDConfigMapName, nil)
	enf.Enforcer.EnableEnforce(false)
	return enf
}

func newClusterNameEnforcer() *rbac.Enforcer {
	enforcer := rbac.NewEnforcer(fake.NewSimpleClientset(test.NewFakeConfigMap()), test.FakeArgoCDNamespace, common.ArgoCDRBACConfigMapName, nil)
	policy := `
	p, alice, clusters, get, minikube, allow
	`
	_ = enforcer.SetBuiltinPolicy(policy)
	rbacEnf := rbacpolicy.NewRBACPolicyEnforcer(enforcer, test.NewFakeProjLister())
	enforcer.SetClaimsEnforcerFunc(rbacEnf.EnforceClaims)
	return enforcer
}

func newClusterServerNameEnforcer() *rbac.Enforcer {
	enforcer := rbac.NewEnforcer(fake.NewSimpleClientset(test.NewFakeConfigMap()), test.FakeArgoCDNamespace, common.ArgoCDRBACConfigMapName, nil)
	enforcer.SetDefaultRole("")
	policy := `
	p, mike, clusters, get, *, allow
	p, mike, clusters, get, https://192.168.0.1, deny
	`
	_ = enforcer.SetBuiltinPolicy(policy)
	rbacEnf := rbacpolicy.NewRBACPolicyEnforcer(enforcer, test.NewFakeProjLister())
	enforcer.SetClaimsEnforcerFunc(rbacEnf.EnforceClaims)
	return enforcer
}

func TestUpdateCluster_NoFieldsPaths(t *testing.T) {
	db := &dbmocks.ArgoDB{}
	var updated *v1alpha1.Cluster
	db.On("UpdateCluster", mock.Anything, mock.MatchedBy(func(c *v1alpha1.Cluster) bool {
		updated = c
		return true
	})).Return(&v1alpha1.Cluster{}, nil)

	server := NewServer(db, newNoopEnforcer(), newServerInMemoryCache(), &kubetest.MockKubectlCmd{})

	_, err := server.Update(context.Background(), &clusterapi.ClusterUpdateRequest{
		Cluster: &v1alpha1.Cluster{
			Name:       "minikube",
			Namespaces: []string{"default", "kube-system"},
		},
	})

	require.NoError(t, err)

	assert.Equal(t, updated.Name, "minikube")
	assert.Equal(t, updated.Namespaces, []string{"default", "kube-system"})
}

func TestUpdateCluster_FieldsPathSet(t *testing.T) {
	db := &dbmocks.ArgoDB{}
	var updated *v1alpha1.Cluster
	db.On("GetCluster", mock.Anything, "https://127.0.0.1").Return(&v1alpha1.Cluster{
		Name:       "minikube",
		Server:     "https://127.0.0.1",
		Namespaces: []string{"default", "kube-system"},
	}, nil)
	db.On("UpdateCluster", mock.Anything, mock.MatchedBy(func(c *v1alpha1.Cluster) bool {
		updated = c
		return true
	})).Return(&v1alpha1.Cluster{}, nil)

	server := NewServer(db, newNoopEnforcer(), newServerInMemoryCache(), &kubetest.MockKubectlCmd{})

	_, err := server.Update(context.Background(), &clusterapi.ClusterUpdateRequest{
		Cluster: &v1alpha1.Cluster{
			Server: "https://127.0.0.1",
			Shard:  pointer.Int64Ptr(1),
		},
		UpdatedFields: []string{"shard"},
	})

	require.NoError(t, err)

	assert.Equal(t, updated.Name, "minikube")
	assert.Equal(t, updated.Namespaces, []string{"default", "kube-system"})
	assert.Equal(t, *updated.Shard, int64(1))
}

func TestGetCluster_EnforcerClusterName(t *testing.T) {
	db := &dbmocks.ArgoDB{}
	db.On("GetCluster", mock.Anything, mock.Anything).Return(&v1alpha1.Cluster{
		ID:                 "id",
		Server:             "https://192.168.0.1",
		Name:               "minikube",
		Namespaces:         []string{"default", "kube-system"},
		Config:             appv1.ClusterConfig{},
		RefreshRequestedAt: nil,
		Shard:              nil,
	}, nil)

	server := NewServer(db, newClusterNameEnforcer(), newServerInMemoryCache(), &kubetest.MockKubectlCmd{})
	ctxA := context.WithValue(context.Background(), "claims", &jwt.StandardClaims{Subject: "alice"})
	c, err := server.Get(ctxA, &clusterapi.ClusterQuery{
		Name:   "minikube",
		Server: "https://192.168.0.1",
	})

	require.NoError(t, err)
	assert.Equal(t, c.Name, "minikube")
	assert.Equal(t, c.Server, "https://192.168.0.1")

}

func TestGetCluster_EnforcerClusterServerName(t *testing.T) {
	db := &dbmocks.ArgoDB{}
	db.On("GetCluster", mock.Anything, mock.Anything).Return(&v1alpha1.Cluster{
		ID:                 "id",
		Server:             "https://192.168.0.1",
		Name:               "minikube",
		Namespaces:         []string{"default", "kube-system"},
		Config:             appv1.ClusterConfig{},
		RefreshRequestedAt: nil,
		Shard:              nil,
	}, nil)

	server := NewServer(db, newClusterServerNameEnforcer(), newServerInMemoryCache(), &kubetest.MockKubectlCmd{})
	ctxA := context.WithValue(context.Background(), "claims", &jwt.StandardClaims{Subject: "mike"})
	_, err := server.Get(ctxA, &clusterapi.ClusterQuery{
		Name:   "minikube",
		Server: "https://192.168.0.1",
	})

	//assert.NotNil(t, err)
	require.NoError(t, err)
}
