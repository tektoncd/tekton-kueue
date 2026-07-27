package controller

import (
	"context"
	_ "embed"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/yaml"
)

const (
	spokeNamespace    = "default"
	saName            = "limited-spoke-sa"
	roleName          = "limited-spoke-role"
	SpokeSecretPrefix = "spoke-kubeconfig-"
)

// MultiKueueReconciler reconciles a target to generate a kubeconfig

type MultiKueueReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// Instructs the Go compiler to read this file and store its contents in this variable
//
//go:embed manifest/spoke-role.yaml
var spokeRoleYAML []byte

// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete

func (r *MultiKueueReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// 1. Fetch the Admin Kubeconfig Secret from the Hub
	var adminSecret corev1.Secret
	if err := r.Get(ctx, req.NamespacedName, &adminSecret); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	adminKubeconfigData, ok := adminSecret.Data["kubeconfig"]
	if !ok {
		logger.Info("Secret does not contain 'kubeconfig' key, ignoring")
		return ctrl.Result{}, nil
	}

	// 2. Build the Spoke Clientset dynamically
	spokeClient, serverURL, err := buildSpokeClient(adminKubeconfigData)
	if err != nil {
		logger.Error(err, "Failed to build spoke client")
		return ctrl.Result{}, err
	}

	// 3. Ensure SA and RBAC exist on Spoke
	if err := r.ensureSpokeRBAC(ctx, spokeClient, spokeNamespace); err != nil {
		logger.Error(err, "Failed to ensure RBAC on spoke")
		return ctrl.Result{}, err
	}

	// 4. Ensure Token Secret exists (K8s 1.24+ requirement)
	if err := r.ensureTokenSecret(ctx, spokeClient, spokeNamespace, saName); err != nil {
		logger.Error(err, "Failed to ensure token secret on spoke")
		return ctrl.Result{}, err
	}

	// 5. Extract Token and CA Cert
	caCert, token, err := r.extractSecretData(ctx, spokeClient, spokeNamespace, saName)
	if err != nil {
		logger.Info("Waiting for Spoke token controller to populate the secret. Requeuing...")
		// Return without error but requeue quickly so we don't spam the logs with errors
		// while waiting for Kubernetes to inject the token data.
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}

	// 6. Build the new Kubeconfig
	newKubeconfigBytes, err := buildKubeconfigYAML(serverURL, req.Name+"-spoke", saName, spokeNamespace, caCert, token)
	if err != nil {
		logger.Error(err, "Failed to build new kubeconfig")
		return ctrl.Result{}, err
	}

	// 7. Create or Update the Resulting Secret on the Hub
	targetSecretName := strings.TrimPrefix(req.Name, SpokeSecretPrefix) + "-limited-kubeconfig"
	logger.Info("Creating a new target secret", "targetSecretName", targetSecretName)

	targetSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      targetSecretName,
			Namespace: "openshift-kueue-operator",
		},
	}

	op, err := controllerutil.CreateOrUpdate(ctx, r.Client, targetSecret, func() error {
		if targetSecret.Data == nil {
			targetSecret.Data = make(map[string][]byte)
		}
		targetSecret.Data["kubeconfig"] = newKubeconfigBytes
		return nil
	})

	if err != nil {
		logger.Error(err, "Failed to create/update limited kubeconfig secret on Hub")
		return ctrl.Result{}, err
	}

	logger.Info(fmt.Sprintf("Successfully processed limited kubeconfig. Operation: %s", op))

	// Because this token is long-lived, we do not need to requeue on a timer.
	// The controller will naturally wake up if the Source Secret (Admin config) changes.
	return ctrl.Result{}, nil
}

func (r *MultiKueueReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Create a filter that strictly checks the namespace and name prefix
	triggerFilter := predicate.NewPredicateFuncs(func(object client.Object) bool {
		return object.GetNamespace() == "default" && strings.HasPrefix(object.GetName(), SpokeSecretPrefix)
	})

	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Secret{}).
		WithEventFilter(triggerFilter). // Apply the filter
		Complete(r)
}

// --- Helper Functions ---

func buildSpokeClient(kubeconfigData []byte) (*kubernetes.Clientset, string, error) {
	restConfig, err := clientcmd.RESTConfigFromKubeConfig(kubeconfigData)
	if err != nil {
		return nil, "", err
	}
	clientset, err := kubernetes.NewForConfig(restConfig)
	return clientset, restConfig.Host, err
}

func (r *MultiKueueReconciler) ensureSpokeRBAC(ctx context.Context, client kubernetes.Interface, ns string) error {
	// Create ServiceAccount
	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{Name: saName},
	}
	if _, err := client.CoreV1().ServiceAccounts(ns).Create(ctx, sa, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("failed to create ServiceAccount: %w", err)
	}

	// 1. Unmarshal the embedded YAML file directly into a ClusterRole struct
	var spokeClusterRole rbacv1.ClusterRole
	if err := yaml.Unmarshal(spokeRoleYAML, &spokeClusterRole); err != nil {
		return fmt.Errorf("failed to parse config/spoke-role.yaml: %w", err)
	}

	// 2. Create or Update ClusterRole (using client.RbacV1().ClusterRoles())
	clusterRoleName := spokeClusterRole.Name
	fmt.Println("Creating a new ClusterRole", clusterRoleName)
	if _, err := client.RbacV1().ClusterRoles().Create(ctx, &spokeClusterRole, metav1.CreateOptions{}); err != nil {
		if apierrors.IsAlreadyExists(err) {
			// If it exists, fetch it and update the rules from our YAML
			existingRole, getErr := client.RbacV1().ClusterRoles().Get(ctx, clusterRoleName, metav1.GetOptions{})
			if getErr != nil {
				return fmt.Errorf("failed to get existing ClusterRole: %w", getErr)
			}

			existingRole.Rules = spokeClusterRole.Rules
			if _, updateErr := client.RbacV1().ClusterRoles().Update(ctx, existingRole, metav1.UpdateOptions{}); updateErr != nil {
				return fmt.Errorf("failed to update ClusterRole: %w", updateErr)
			}
		} else {
			return fmt.Errorf("failed to create ClusterRole: %w", err)
		}
	}

	// 3. Create or Update ClusterRoleBinding
	bindingName := saName + "-clusterbinding"
	expectedSubjects := []rbacv1.Subject{{
		Kind:      "ServiceAccount",
		Name:      saName,
		Namespace: ns, // SA lives in the namespace, even though the binding is cluster-wide
	}}
	expectedRoleRef := rbacv1.RoleRef{
		Kind:     "ClusterRole",
		Name:     clusterRoleName, // Bind to the ClusterRole we just parsed from YAML
		APIGroup: "rbac.authorization.k8s.io",
	}

	binding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: bindingName},
		Subjects:   expectedSubjects,
		RoleRef:    expectedRoleRef,
	}

	if _, err := client.RbacV1().ClusterRoleBindings().Create(ctx, binding, metav1.CreateOptions{}); err != nil {
		if apierrors.IsAlreadyExists(err) {
			// If it exists, fetch and update
			existingBinding, getErr := client.RbacV1().ClusterRoleBindings().Get(ctx, bindingName, metav1.GetOptions{})
			if getErr != nil {
				return fmt.Errorf("failed to get existing ClusterRoleBinding: %w", getErr)
			}

			existingBinding.Subjects = expectedSubjects
			existingBinding.RoleRef = expectedRoleRef
			if _, updateErr := client.RbacV1().ClusterRoleBindings().Update(ctx, existingBinding, metav1.UpdateOptions{}); updateErr != nil {
				return fmt.Errorf("failed to update ClusterRoleBinding: %w", updateErr)
			}
		} else {
			return fmt.Errorf("failed to create ClusterRoleBinding: %w", err)
		}
	}

	return nil
}

func (r *MultiKueueReconciler) ensureTokenSecret(ctx context.Context, client *kubernetes.Clientset, ns, sa string) error {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:        sa + "-token",
			Annotations: map[string]string{"kubernetes.io/service-account.name": sa},
		},
		Type: corev1.SecretTypeServiceAccountToken,
	}
	_, err := client.CoreV1().Secrets(ns).Create(ctx, secret, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

func (r *MultiKueueReconciler) extractSecretData(ctx context.Context, client *kubernetes.Clientset, ns, sa string) ([]byte, string, error) {
	secretName := sa + "-token"
	secret, err := client.CoreV1().Secrets(ns).Get(ctx, secretName, metav1.GetOptions{})
	if err != nil {
		return nil, "", err
	}

	caCert := secret.Data["ca.crt"]
	token := secret.Data["token"]

	if len(caCert) == 0 || len(token) == 0 {
		return nil, "", fmt.Errorf("token or ca.crt not yet populated in secret")
	}

	return caCert, string(token), nil
}

func buildKubeconfigYAML(server, clusterName, user, ns string, caCert []byte, token string) ([]byte, error) {
	config := clientcmdapi.NewConfig()
	config.Clusters[clusterName] = clientcmdapi.NewCluster()
	config.Clusters[clusterName].Server = server
	config.Clusters[clusterName].CertificateAuthorityData = caCert

	config.AuthInfos[user] = clientcmdapi.NewAuthInfo()
	config.AuthInfos[user].Token = token

	contextName := user + "-context"
	config.Contexts[contextName] = clientcmdapi.NewContext()
	config.Contexts[contextName].Cluster = clusterName
	config.Contexts[contextName].AuthInfo = user
	config.Contexts[contextName].Namespace = ns
	config.CurrentContext = contextName

	return clientcmd.Write(*config)
}
