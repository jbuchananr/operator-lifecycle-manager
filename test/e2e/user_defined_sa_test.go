package e2e

import (
	"context"
	"fmt"

	"github.com/blang/semver/v4"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	v1 "github.com/operator-framework/api/pkg/operators/v1"
	"github.com/operator-framework/api/pkg/operators/v1alpha1"
	"github.com/operator-framework/operator-lifecycle-manager/pkg/api/client/clientset/versioned"
	"github.com/operator-framework/operator-lifecycle-manager/pkg/controller/registry"
	"github.com/operator-framework/operator-lifecycle-manager/pkg/lib/kubernetes/pkg/apis/rbac"
	"github.com/operator-framework/operator-lifecycle-manager/pkg/lib/operatorclient"
	"github.com/operator-framework/operator-lifecycle-manager/test/e2e/ctx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var _ = Describe("User defined service account", func() {
	var (
		generatedNamespace corev1.Namespace
	)

	BeforeEach(func() {
		generatedNamespace = corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: genName("user-defined-sa-e2e-"),
			},
		}
		Eventually(func() error {
			return ctx.Ctx().Client().Create(context.Background(), &generatedNamespace)
		}).Should(Succeed())
	})

	AfterEach(func() {
		TeardownNamespace(generatedNamespace.GetName())
	})

	It("with no permission", func() {

		kubeclient := newKubeClient()
		crclient := newCRClient()

		// Create a service account, but add no permission to it.
		saName := genName("scoped-sa-")
		_, cleanupSA := newServiceAccount(kubeclient, generatedNamespace.GetName(), saName)
		defer cleanupSA()

		// Add an OperatorGroup and specify the service account.
		ogName := genName("scoped-og-")
		_, cleanupOG := newOperatorGroupWithServiceAccount(crclient, generatedNamespace.GetName(), ogName, saName)
		defer cleanupOG()

		permissions := deploymentPermissions()
		catsrc, subSpec, catsrcCleanup := newCatalogSource(GinkgoT(), kubeclient, crclient, "scoped", generatedNamespace.GetName(), permissions)
		defer catsrcCleanup()

		// Ensure that the catalog source is resolved before we create a subscription.
		_, err := fetchCatalogSourceOnStatus(crclient, catsrc.GetName(), generatedNamespace.GetName(), catalogSourceRegistryPodSynced)
		require.NoError(GinkgoT(), err)

		subscriptionName := genName("scoped-sub-")
		cleanupSubscription := createSubscriptionForCatalog(crclient, generatedNamespace.GetName(), subscriptionName, catsrc.GetName(), subSpec.Package, subSpec.Channel, subSpec.StartingCSV, subSpec.InstallPlanApproval)
		defer cleanupSubscription()

		// Wait until an install plan is created.
		subscription, err := fetchSubscription(crclient, generatedNamespace.GetName(), subscriptionName, subscriptionHasInstallPlanChecker)
		require.NoError(GinkgoT(), err)
		require.NotNil(GinkgoT(), subscription)

		// We expect the InstallPlan to be in status: Failed.
		ipName := subscription.Status.Install.Name
		ipPhaseCheckerFunc := buildInstallPlanPhaseCheckFunc(v1alpha1.InstallPlanPhaseFailed)
		ipGot, err := fetchInstallPlanWithNamespace(GinkgoT(), crclient, ipName, generatedNamespace.GetName(), ipPhaseCheckerFunc)
		require.NoError(GinkgoT(), err)

		conditionGot := mustHaveCondition(GinkgoT(), ipGot, v1alpha1.InstallPlanInstalled)
		assert.Equal(GinkgoT(), corev1.ConditionFalse, conditionGot.Status)
		assert.Equal(GinkgoT(), v1alpha1.InstallPlanReasonComponentFailed, conditionGot.Reason)
		assert.Contains(GinkgoT(), conditionGot.Message, fmt.Sprintf("is forbidden: User \"system:serviceaccount:%s:%s\" cannot create resource", generatedNamespace.GetName(), saName))

		// Verify that all step resources are in Unknown state.
		for _, step := range ipGot.Status.Plan {
			assert.Equal(GinkgoT(), v1alpha1.StepStatusUnknown, step.Status)
		}
	})
	It("with permission", func() {

		// Create the CatalogSource
		kubeclient := newKubeClient()
		crclient := newCRClient()

		// Create a service account, add enough permission to it so that operator install is successful.
		saName := genName("scoped-sa")
		_, cleanupSA := newServiceAccount(kubeclient, generatedNamespace.GetName(), saName)
		defer cleanupSA()
		cleanupPerm := grantPermission(GinkgoT(), kubeclient, generatedNamespace.GetName(), saName)
		defer cleanupPerm()

		// Add an OperatorGroup and specify the service account.
		ogName := genName("scoped-og-")
		_, cleanupOG := newOperatorGroupWithServiceAccount(crclient, generatedNamespace.GetName(), ogName, saName)
		defer cleanupOG()

		permissions := deploymentPermissions()
		catsrc, subSpec, catsrcCleanup := newCatalogSource(GinkgoT(), kubeclient, crclient, "scoped", generatedNamespace.GetName(), permissions)
		defer catsrcCleanup()

		// Ensure that the catalog source is resolved before we create a subscription.
		_, err := fetchCatalogSourceOnStatus(crclient, catsrc.GetName(), generatedNamespace.GetName(), catalogSourceRegistryPodSynced)
		require.NoError(GinkgoT(), err)

		subscriptionName := genName("scoped-sub-")
		cleanupSubscription := createSubscriptionForCatalog(crclient, generatedNamespace.GetName(), subscriptionName, catsrc.GetName(), subSpec.Package, subSpec.Channel, subSpec.StartingCSV, subSpec.InstallPlanApproval)
		defer cleanupSubscription()

		// Wait until an install plan is created.
		subscription, err := fetchSubscription(crclient, generatedNamespace.GetName(), subscriptionName, subscriptionHasInstallPlanChecker)
		require.NoError(GinkgoT(), err)
		require.NotNil(GinkgoT(), subscription)

		// We expect the InstallPlan to be in status: Complete.
		ipName := subscription.Status.Install.Name
		ipPhaseCheckerFunc := buildInstallPlanPhaseCheckFunc(v1alpha1.InstallPlanPhaseComplete)
		ipGot, err := fetchInstallPlanWithNamespace(GinkgoT(), crclient, ipName, generatedNamespace.GetName(), ipPhaseCheckerFunc)
		require.NoError(GinkgoT(), err)

		conditionGot := mustHaveCondition(GinkgoT(), ipGot, v1alpha1.InstallPlanInstalled)
		assert.Equal(GinkgoT(), v1alpha1.InstallPlanConditionReason(""), conditionGot.Reason)
		assert.Equal(GinkgoT(), corev1.ConditionTrue, conditionGot.Status)
		assert.Equal(GinkgoT(), "", conditionGot.Message)

		// Verify that all step resources are in Created state.
		for _, step := range ipGot.Status.Plan {
			// TODO: switch back to commented assertion once InstallPlan status is being patched instead of updated
			// assert.Equal(GinkgoT(), v1alpha1.StepStatusCreated, step.Status)
			Expect(step.Status).To(Or(Equal(v1alpha1.StepStatusCreated), Equal(v1alpha1.StepStatusPresent)))
		}
	})
	It("with retry", func() {

		kubeclient := newKubeClient()
		crclient := newCRClient()

		// Create a service account, but add no permission to it.
		saName := genName("scoped-sa-")
		_, cleanupSA := newServiceAccount(kubeclient, generatedNamespace.GetName(), saName)
		defer cleanupSA()

		// Add an OperatorGroup and specify the service account.
		ogName := genName("scoped-og-")
		_, cleanupOG := newOperatorGroupWithServiceAccount(crclient, generatedNamespace.GetName(), ogName, saName)
		defer cleanupOG()

		permissions := deploymentPermissions()
		catsrc, subSpec, catsrcCleanup := newCatalogSource(GinkgoT(), kubeclient, crclient, "scoped", generatedNamespace.GetName(), permissions)
		defer catsrcCleanup()

		// Ensure that the catalog source is resolved before we create a subscription.
		_, err := fetchCatalogSourceOnStatus(crclient, catsrc.GetName(), generatedNamespace.GetName(), catalogSourceRegistryPodSynced)
		require.NoError(GinkgoT(), err)

		subscriptionName := genName("scoped-sub-")
		cleanupSubscription := createSubscriptionForCatalog(crclient, generatedNamespace.GetName(), subscriptionName, catsrc.GetName(), subSpec.Package, subSpec.Channel, subSpec.StartingCSV, subSpec.InstallPlanApproval)
		defer cleanupSubscription()

		// Wait until an install plan is created.
		subscription, err := fetchSubscription(crclient, generatedNamespace.GetName(), subscriptionName, subscriptionHasInstallPlanChecker)
		require.NoError(GinkgoT(), err)
		require.NotNil(GinkgoT(), subscription)

		// We expect the InstallPlan to be in status: Failed.
		ipNameOld := subscription.Status.InstallPlanRef.Name
		ipPhaseCheckerFunc := buildInstallPlanPhaseCheckFunc(v1alpha1.InstallPlanPhaseFailed)
		ipGotOld, err := fetchInstallPlanWithNamespace(GinkgoT(), crclient, ipNameOld, generatedNamespace.GetName(), ipPhaseCheckerFunc)
		require.NoError(GinkgoT(), err)
		require.Equal(GinkgoT(), v1alpha1.InstallPlanPhaseFailed, ipGotOld.Status.Phase)

		// Grant permission now and this should trigger an retry of InstallPlan.
		cleanupPerm := grantPermission(GinkgoT(), kubeclient, generatedNamespace.GetName(), saName)
		defer cleanupPerm()

		ipPhaseCheckerFunc = buildInstallPlanPhaseCheckFunc(v1alpha1.InstallPlanPhaseComplete)
		ipGotNew, err := fetchInstallPlanWithNamespace(GinkgoT(), crclient, ipNameOld, generatedNamespace.GetName(), ipPhaseCheckerFunc)
		require.NoError(GinkgoT(), err)
		require.Equal(GinkgoT(), v1alpha1.InstallPlanPhaseComplete, ipGotNew.Status.Phase)
	})
})

func newNamespace(client operatorclient.ClientInterface, name string) (ns *corev1.Namespace, cleanup cleanupFunc) {
	request := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
	}

	Eventually(func() (err error) {
		ns, err = client.KubernetesInterface().CoreV1().Namespaces().Create(context.TODO(), request, metav1.CreateOptions{})
		return
	}).Should(Succeed())

	cleanup = func() {
		Eventually(func() error {
			err := client.KubernetesInterface().CoreV1().Namespaces().Delete(context.TODO(), ns.GetName(), metav1.DeleteOptions{})
			if apierrors.IsNotFound(err) {
				err = nil
			}

			return err
		}).Should(Succeed())
	}

	return
}

func newServiceAccount(client operatorclient.ClientInterface, namespace, name string) (sa *corev1.ServiceAccount, cleanup cleanupFunc) {
	request := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      name,
		},
	}

	sa, err := client.KubernetesInterface().CoreV1().ServiceAccounts(namespace).Create(context.TODO(), request, metav1.CreateOptions{})
	Expect(err).ToNot(HaveOccurred())
	Expect(sa).ToNot(BeNil())

	cleanup = func() {
		err := client.KubernetesInterface().CoreV1().ServiceAccounts(sa.GetNamespace()).Delete(context.TODO(), sa.GetName(), metav1.DeleteOptions{})
		Expect(err).ToNot(HaveOccurred())
	}

	return
}

func newOperatorGroupWithServiceAccount(client versioned.Interface, namespace, name, serviceAccountName string) (og *v1.OperatorGroup, cleanup cleanupFunc) {
	request := &v1.OperatorGroup{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      name,
		},
		Spec: v1.OperatorGroupSpec{
			TargetNamespaces: []string{
				namespace,
			},
			ServiceAccountName: serviceAccountName,
		},
	}

	og, err := client.OperatorsV1().OperatorGroups(namespace).Create(context.TODO(), request, metav1.CreateOptions{})
	Expect(err).ToNot(HaveOccurred())
	Expect(og).ToNot(BeNil())

	cleanup = func() {
		err := client.OperatorsV1().OperatorGroups(og.GetNamespace()).Delete(context.TODO(), og.GetName(), metav1.DeleteOptions{})
		Expect(err).ToNot(HaveOccurred())
	}

	return
}

func newCatalogSource(t GinkgoTInterface, kubeclient operatorclient.ClientInterface, crclient versioned.Interface, prefix, namespace string, permissions []v1alpha1.StrategyDeploymentPermissions) (catsrc *v1alpha1.CatalogSource, subscriptionSpec *v1alpha1.SubscriptionSpec, cleanup cleanupFunc) {
	crdPlural := genName("ins")
	crdName := crdPlural + ".cluster.com"

	crd := apiextensions.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name: crdName,
		},
		Spec: apiextensions.CustomResourceDefinitionSpec{
			Group: "cluster.com",
			Versions: []apiextensions.CustomResourceDefinitionVersion{
				{
					Name:    "v1alpha1",
					Served:  true,
					Storage: true,
					Schema: &apiextensions.CustomResourceValidation{
						OpenAPIV3Schema: &apiextensions.JSONSchemaProps{
							Type:        "object",
							Description: "my crd schema",
						},
					},
				},
			},
			Names: apiextensions.CustomResourceDefinitionNames{
				Plural:   crdPlural,
				Singular: crdPlural,
				Kind:     crdPlural,
				ListKind: "list" + crdPlural,
			},
			Scope: apiextensions.NamespaceScoped,
		},
	}

	prefixFunc := func(s string) string {
		return fmt.Sprintf("%s-%s-", prefix, s)
	}

	// Create CSV
	packageName := genName(prefixFunc("package"))
	stableChannel := "stable"

	namedStrategy := newNginxInstallStrategy(genName(prefixFunc("dep")), permissions, nil)
	csvA := newCSV("nginx-a", namespace, "", semver.MustParse("0.1.0"), []apiextensions.CustomResourceDefinition{crd}, nil, &namedStrategy)
	csvB := newCSV("nginx-b", namespace, "nginx-a", semver.MustParse("0.2.0"), []apiextensions.CustomResourceDefinition{crd}, nil, &namedStrategy)

	// Create PackageManifests
	manifests := []registry.PackageManifest{
		{
			PackageName: packageName,
			Channels: []registry.PackageChannel{
				{Name: stableChannel, CurrentCSVName: csvB.GetName()},
			},
			DefaultChannelName: stableChannel,
		},
	}

	catalogSourceName := genName(prefixFunc("catsrc"))
	catsrc, cleanup = createInternalCatalogSource(kubeclient, crclient, catalogSourceName, namespace, manifests, []apiextensions.CustomResourceDefinition{crd}, []v1alpha1.ClusterServiceVersion{csvA, csvB})
	require.NotNil(t, catsrc)
	require.NotNil(t, cleanup)

	subscriptionSpec = &v1alpha1.SubscriptionSpec{
		CatalogSource:          catsrc.GetName(),
		CatalogSourceNamespace: catsrc.GetNamespace(),
		Package:                packageName,
		Channel:                stableChannel,
		StartingCSV:            csvB.GetName(),
		InstallPlanApproval:    v1alpha1.ApprovalAutomatic,
	}
	return
}

func newCatalogSourceWithDependencies(t GinkgoTInterface, kubeclient operatorclient.ClientInterface, crclient versioned.Interface, prefix, namespace string, permissions []v1alpha1.StrategyDeploymentPermissions) (catsrc *v1alpha1.CatalogSource, subscriptionSpec *v1alpha1.SubscriptionSpec, cleanup cleanupFunc) {
	crdPlural := genName("ins")
	crdName := crdPlural + ".cluster.com"

	crd := apiextensions.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name: crdName,
		},
		Spec: apiextensions.CustomResourceDefinitionSpec{
			Group: "cluster.com",
			Versions: []apiextensions.CustomResourceDefinitionVersion{
				{
					Name:    "v1alpha1",
					Served:  true,
					Storage: true,
					Schema: &apiextensions.CustomResourceValidation{
						OpenAPIV3Schema: &apiextensions.JSONSchemaProps{
							Type:        "object",
							Description: "my crd schema",
						},
					},
				},
			},
			Names: apiextensions.CustomResourceDefinitionNames{
				Plural:   crdPlural,
				Singular: crdPlural,
				Kind:     crdPlural,
				ListKind: "list" + crdPlural,
			},
			Scope: apiextensions.NamespaceScoped,
		},
	}

	prefixFunc := func(s string) string {
		return fmt.Sprintf("%s-%s-", prefix, s)
	}

	// Create CSV
	packageName1 := genName(prefixFunc("package"))
	packageName2 := genName(prefixFunc("package"))
	stableChannel := "stable"

	namedStrategy := newNginxInstallStrategy(genName(prefixFunc("dep")), permissions, nil)
	csvA := newCSV("nginx-req-dep", namespace, "", semver.MustParse("0.1.0"), nil, []apiextensions.CustomResourceDefinition{crd}, &namedStrategy)
	csvB := newCSV("nginx-dependency", namespace, "", semver.MustParse("0.1.0"), []apiextensions.CustomResourceDefinition{crd}, nil, &namedStrategy)

	// Create PackageManifests
	manifests := []registry.PackageManifest{
		{
			PackageName: packageName1,
			Channels: []registry.PackageChannel{
				{Name: stableChannel, CurrentCSVName: csvA.GetName()},
			},
			DefaultChannelName: stableChannel,
		},
		{
			PackageName: packageName2,
			Channels: []registry.PackageChannel{
				{Name: stableChannel, CurrentCSVName: csvB.GetName()},
			},
			DefaultChannelName: stableChannel,
		},
	}

	catalogSourceName := genName(prefixFunc("catsrc"))
	catsrc, cleanup = createInternalCatalogSource(kubeclient, crclient, catalogSourceName, namespace, manifests, []apiextensions.CustomResourceDefinition{crd}, []v1alpha1.ClusterServiceVersion{csvA, csvB})
	require.NotNil(t, catsrc)
	require.NotNil(t, cleanup)

	subscriptionSpec = &v1alpha1.SubscriptionSpec{
		CatalogSource:          catsrc.GetName(),
		CatalogSourceNamespace: catsrc.GetNamespace(),
		Package:                packageName1,
		Channel:                stableChannel,
		StartingCSV:            csvA.GetName(),
		InstallPlanApproval:    v1alpha1.ApprovalAutomatic,
	}
	return
}

func mustHaveCondition(t GinkgoTInterface, ip *v1alpha1.InstallPlan, conditionType v1alpha1.InstallPlanConditionType) (condition *v1alpha1.InstallPlanCondition) {
	for i := range ip.Status.Conditions {
		if ip.Status.Conditions[i].Type == conditionType {
			condition = &ip.Status.Conditions[i]
			break
		}
	}

	require.NotNil(t, condition)
	return
}

func deploymentPermissions() []v1alpha1.StrategyDeploymentPermissions {
	// Generate permissions
	serviceAccountName := genName("nginx-sa-")
	permissions := []v1alpha1.StrategyDeploymentPermissions{
		{
			ServiceAccountName: serviceAccountName,
			Rules: []rbacv1.PolicyRule{
				{
					Verbs:     []string{rbac.VerbAll},
					APIGroups: []string{rbac.APIGroupAll},
					Resources: []string{rbac.ResourceAll}},
			},
		},
	}

	return permissions
}

func grantPermission(t GinkgoTInterface, client operatorclient.ClientInterface, namespace, serviceAccountName string) (cleanup cleanupFunc) {
	role := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:      genName("scoped-role-"),
			Namespace: namespace,
		},
		Rules: []rbacv1.PolicyRule{
			{
				Verbs:     []string{rbac.VerbAll},
				APIGroups: []string{rbac.APIGroupAll},
				Resources: []string{rbac.ResourceAll},
			},
		},
	}

	role, err := client.KubernetesInterface().RbacV1().Roles(namespace).Create(context.TODO(), role, metav1.CreateOptions{})
	require.NoError(t, err)

	clusterrole := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name:      genName("scoped-clusterrole-"),
			Namespace: namespace},
		Rules: []rbacv1.PolicyRule{
			{
				Verbs:     []string{rbac.VerbAll},
				APIGroups: []string{rbac.APIGroupAll},
				Resources: []string{rbac.ResourceAll},
			},
		},
	}

	clusterrole, err = client.KubernetesInterface().RbacV1().ClusterRoles().Create(context.TODO(), clusterrole, metav1.CreateOptions{})
	require.NoError(t, err)

	binding := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      genName("scoped-rolebinding-"),
			Namespace: namespace,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				APIGroup:  "",
				Name:      serviceAccountName,
				Namespace: namespace,
			},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "Role",
			Name:     role.GetName(),
		},
	}

	clusterbinding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      genName("scoped-clusterrolebinding-"),
			Namespace: namespace,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				APIGroup:  "",
				Name:      serviceAccountName,
				Namespace: namespace,
			},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     clusterrole.GetName(),
		},
	}

	binding, err = client.KubernetesInterface().RbacV1().RoleBindings(namespace).Create(context.TODO(), binding, metav1.CreateOptions{})
	require.NoError(t, err)

	clusterbinding, err = client.KubernetesInterface().RbacV1().ClusterRoleBindings().Create(context.TODO(), clusterbinding, metav1.CreateOptions{})
	require.NoError(t, err)

	cleanup = func() {
		err := client.KubernetesInterface().RbacV1().Roles(role.GetNamespace()).Delete(context.TODO(), role.GetName(), metav1.DeleteOptions{})
		require.NoError(t, err)

		err = client.KubernetesInterface().RbacV1().RoleBindings(binding.GetNamespace()).Delete(context.TODO(), binding.GetName(), metav1.DeleteOptions{})
		require.NoError(t, err)

		err = client.KubernetesInterface().RbacV1().ClusterRoles().Delete(context.TODO(), clusterrole.GetName(), metav1.DeleteOptions{})
		require.NoError(t, err)

		err = client.KubernetesInterface().RbacV1().ClusterRoleBindings().Delete(context.TODO(), clusterbinding.GetName(), metav1.DeleteOptions{})
		require.NoError(t, err)
	}

	return
}
