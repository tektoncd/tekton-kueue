/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/konflux-ci/tekton-kueue/pkg/common"
	"github.com/konflux-ci/tekton-kueue/pkg/config"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gopkg.in/yaml.v3"

	"github.com/konflux-ci/tekton-kueue/test/utils"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	tekv1 "github.com/tektoncd/pipeline/pkg/apis/pipeline/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kerrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	kapi "knative.dev/pkg/apis"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
)

// namespace where the project is deployed in
const namespace = "tekton-kueue"

// metricsRoleBindingName is the name of the RBAC that will be created to allow get the metrics data
const metricsRoleBindingName = "tekton-kueue-metrics-binding"

type PodNameGetter = func() string
type PodNameSetter = func(string)

type TestContext struct {
	ControllerPodName string
	WebhookPodName    string
}

func (tc *TestContext) GetControllerPodName() string {
	return tc.ControllerPodName
}

func (tc *TestContext) SetControllerPodName(name string) {
	tc.ControllerPodName = name
}

func (tc *TestContext) GetWebhookPodName() string {
	return tc.WebhookPodName
}

func (tc *TestContext) SetWebhookPodName(name string) {
	tc.WebhookPodName = name
}

func GetCurlMetricsPodName(fromPodName string) string {
	return fmt.Sprintf("curl-metrics-%s", fromPodName)
}

var _ = Describe("Manager", Ordered, func() {
	testContext := &TestContext{}
	var k8sClient client.Client
	nsName := "test-ns"

	// Before running the tests, set up the environment by creating the namespace,
	// enforce the restricted security policy to the namespace, installing CRDs,
	// and deploying the controller.
	BeforeAll(func(ctx context.Context) {
		By("creating manager namespace")
		cmd := exec.Command("kubectl", "create", "ns", namespace)
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to create namespace")

		By("labeling the namespace to enforce the restricted security policy")
		cmd = exec.Command("kubectl", "label", "--overwrite", "ns", namespace,
			"pod-security.kubernetes.io/enforce=restricted")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to label namespace with restricted policy")

		By("installing CRDs")
		cmd = exec.Command("make", "install")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to install CRDs")

		By("deploying the controller-manager")
		projectImage := os.Getenv("IMG")
		Expect(projectImage).ToNot(Equal(""), "IMG environment variable must be declared")
		cmd = exec.Command("make", "deploy", fmt.Sprintf("IMG=%s", projectImage))
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to deploy the controller-manager")

		By("Creating a k8s client")
		// The context provided by the callback is closed when it's completed,
		// so we need to create another context for the client.
		k8sClient = getK8sClientOrDie(context.Background())

		By(fmt.Sprintf("Creating a namespace: %s", nsName), func() {
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: nsName,
				},
			}
			Expect(k8sClient.Create(ctx, ns)).To(Satisfy(func(err error) bool {
				return err == nil || kerrors.IsAlreadyExists(err)
			}))
		})

		By("Deploying ResourceFlavor, ClusterQueue and Local Queue", func() {
			cmd := exec.Command(
				"kubectl",
				"apply",
				"--server-side",
				"-n",
				nsName,
				"-f",
				"config/samples/kueue/kueue-resources.yaml",
			)
			_, err := utils.Run(cmd)
			Expect(err).To(Succeed(), "Failed to apply kueue resources")
		})
	})

	// After all tests have been executed, clean up by undeploying the controller, uninstalling CRDs,
	// and deleting the namespace. Set SKIP_CLEANUP=true to keep resources running (e.g., for
	// collecting coverage data from instrumented pods before teardown).
	AfterAll(func() {
		if os.Getenv("SKIP_CLEANUP") == "true" { //nolint:goconst
			By("skipping cleanup because SKIP_CLEANUP is set")
			return
		}

		By("cleaning up the curl pod for metrics")
		cmd := exec.Command("kubectl", "delete", "pod", "curl-metrics", "-n", namespace)
		_, _ = utils.Run(cmd)

		By("undeploying the controller-manager")
		cmd = exec.Command("make", "undeploy")
		_, _ = utils.Run(cmd)

		By("uninstalling CRDs")
		cmd = exec.Command("make", "uninstall")
		_, _ = utils.Run(cmd)

		By("removing manager namespace")
		cmd = exec.Command("kubectl", "delete", "ns", namespace)
		_, _ = utils.Run(cmd)
	})

	// After each test, check for failures and collect logs, events,
	// and pod descriptions for debugging.
	AfterEach(func() {
		specReport := CurrentSpecReport()
		if specReport.Failed() {
			for _, podName := range []string{testContext.ControllerPodName, testContext.WebhookPodName} {

				By(fmt.Sprintf("Fetching %s pod logs", podName))
				cmd := exec.Command("kubectl", "logs", podName, "-n", namespace)
				logs, err := utils.Run(cmd)
				if err == nil {
					_, _ = fmt.Fprintf(GinkgoWriter, "pod logs:\n %s", logs)
				} else {
					_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get pod logs: %s", err)
				}

				By(fmt.Sprintf("Fetching %s description\n", podName))
				cmd = exec.Command("kubectl", "describe", podName, "-n", namespace)
				podDescription, err := utils.Run(cmd)
				if err == nil {
					_, _ = fmt.Fprintf(GinkgoWriter, "Pod description: %s\n", podDescription)
				} else {
					_, _ = fmt.Fprintf(GinkgoWriter, "Failed to describe pod %s\n", podName)
				}
			}

			By("Fetching Kubernetes events")
			cmd := exec.Command("kubectl", "get", "events", "-n", namespace, "--sort-by=.lastTimestamp")
			eventsOutput, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Kubernetes events:\n%s", eventsOutput)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get Kubernetes events: %s", err)
			}

			for _, podName := range []string{testContext.ControllerPodName, testContext.WebhookPodName} {
				curlPod := GetCurlMetricsPodName(podName)
				By(fmt.Sprintf("Fetching %s logs", curlPod))
				cmd = exec.Command("kubectl", "logs", curlPod, "-n", namespace)
				metricsOutput, err := utils.Run(cmd)
				if err == nil {
					_, _ = fmt.Fprintf(GinkgoWriter, "Metrics logs:\n %s", metricsOutput)
				} else {
					_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get curl-metrics logs: %s", err)
				}
			}

			By("Fetching PipelineRuns")
			cmd = exec.Command("kubectl", "get", "-A", "-o", "yaml", "pipelineruns")
			pipelineruns, err := utils.Run(cmd)
			if err == nil {
				fmt.Println("pipelinruns:\n", pipelineruns)
			} else {
				fmt.Println("Failed to get pipelinruns")
			}

			By("Fetching Workloads")
			cmd = exec.Command("kubectl", "get", "-A", "-o", "yaml", "workloads")
			workloads, err := utils.Run(cmd)
			if err == nil {
				fmt.Println("workloads:\n", workloads)
			} else {
				fmt.Println("Failed to get workloads")
			}
		}
	})

	SetDefaultEventuallyTimeout(2 * time.Minute)
	SetDefaultEventuallyPollingInterval(time.Second)

	plrTemplate := &tekv1.PipelineRun{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "pipeline-",
			Namespace:    "test-ns",
		},
		Spec: tekv1.PipelineRunSpec{
			PipelineSpec: &tekv1.PipelineSpec{
				Tasks: []tekv1.PipelineTask{
					{
						Name: "hello-world",
						TaskSpec: &tekv1.EmbeddedTask{
							TaskSpec: tekv1.TaskSpec{
								Steps: []tekv1.Step{
									{
										Name:    "hello-world",
										Image:   "registry.access.redhat.com/ubi9/ubi-micro:latest",
										Command: []string{"echo", "hello-world"},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	Context("Manager", func() {
		DescribeTable(
			"should run successfully",
			func(label string, nameSubstring string, podNameSetter PodNameSetter) {
				By("validating that the controller-manager pod is running as expected")
				verifyControllerUp := func(g Gomega) {
					// Get the name of the controller-manager pod
					cmd := exec.Command("kubectl", "get",
						"pods", "-l", label,
						"-o", "go-template={{ range .items }}"+
							"{{ if not .metadata.deletionTimestamp }}"+
							"{{ .metadata.name }}"+
							"{{ \"\\n\" }}{{ end }}{{ end }}",
						"-n", namespace,
					)

					podOutput, err := utils.Run(cmd)
					g.Expect(err).NotTo(HaveOccurred(), "Failed to retrieve pod information")
					podNames := utils.GetNonEmptyLines(podOutput)
					g.Expect(podNames).To(HaveLen(1), "expected 1 pod running")
					podName := podNames[0]
					g.Expect(podName).To(ContainSubstring(nameSubstring))

					// Validate the pod's status
					cmd = exec.Command("kubectl", "get",
						"pods", podName, "-o", "jsonpath={.status.phase}",
						"-n", namespace,
					)
					output, err := utils.Run(cmd)
					g.Expect(err).NotTo(HaveOccurred())
					g.Expect(output).To(Equal("Running"), "Incorrect pod status")

					podNameSetter(podName)

				}
				Eventually(verifyControllerUp).Should(Succeed())
			},
			Entry("controller", "app.kubernetes.io/name=tekton-kueue", "controller-manager", testContext.SetControllerPodName),
			Entry("webhook", "app.kubernetes.io/name=tekton-kueue-webhook", "webhook", testContext.SetWebhookPodName),
		)

		DescribeTable(
			"should ensure the metrics endpoint is serving metrics",
			func(metricsServiceName, serviceAccountName, metricsSubstring string, getPodName PodNameGetter) {
				By("creating a ClusterRoleBinding for the service account to allow access to metrics")
				cmd := exec.Command(
					"kubectl",
					"create",
					"clusterrolebinding",
					"--dry-run=client",
					"-o",
					"yaml",
					metricsRoleBindingName,
					"--clusterrole=tekton-kueue-metrics-reader",
					fmt.Sprintf("--serviceaccount=%s:%s", namespace, serviceAccountName),
				)
				crb, err := utils.Run(cmd)
				Expect(err).NotTo(HaveOccurred(), "Failed to generate ClusterRoleBinding")

				cmd = exec.Command("kubectl", "apply", "-f", "-")
				cmd.Stdin = strings.NewReader(crb)
				Expect(utils.Run(cmd)).Error().NotTo(HaveOccurred(), "Failed to apply ClusterRoleBinding")

				By("validating that the metrics service is available")
				cmd = exec.Command("kubectl", "get", "service", metricsServiceName, "-n", namespace)
				Expect(utils.Run(cmd)).Error().NotTo(HaveOccurred(), "Metrics service should exist")

				By("validating that the ServiceMonitor for Prometheus is applied in the namespace")
				cmd = exec.Command("kubectl", "get", "ServiceMonitor", "-n", namespace)
				Expect(utils.Run(cmd)).Error().NotTo(HaveOccurred(), "ServiceMonitor should exist")

				By("getting the service account token")
				token, err := serviceAccountToken(serviceAccountName)
				Expect(err).NotTo(HaveOccurred())
				Expect(token).NotTo(BeEmpty())

				By("waiting for the metrics endpoint to be ready")
				verifyMetricsEndpointReady := func(g Gomega) {
					cmd := exec.Command("kubectl", "get", "endpoints", metricsServiceName, "-n", namespace)
					output, err := utils.Run(cmd)
					g.Expect(err).NotTo(HaveOccurred())
					g.Expect(output).To(ContainSubstring("8443"), "Metrics endpoint is not ready")
				}
				Eventually(verifyMetricsEndpointReady).Should(Succeed())

				By("verifying that the controller manager is serving the metrics server")
				verifyMetricsServerStarted := func(g Gomega) {
					cmd := exec.Command("kubectl", "logs", getPodName(), "-n", namespace)
					output, err := utils.Run(cmd)
					g.Expect(err).NotTo(HaveOccurred())
					g.Expect(output).To(ContainSubstring("controller-runtime.metrics\tServing metrics server"),
						"Metrics server not yet started")
				}
				Eventually(verifyMetricsServerStarted).Should(Succeed())

				By("creating the curl-metrics pod to access the metrics endpoint")
				cmd = exec.Command("kubectl", "run", GetCurlMetricsPodName(getPodName()), "--restart=Never",
					"--namespace", namespace,
					"--image=curlimages/curl:latest",
					"--overrides",
					fmt.Sprintf(`{
					"spec": {
						"containers": [{
							"name": "curl",
							"image": "curlimages/curl:latest",
							"command": ["/bin/sh", "-c"],
							"args": ["curl -v -k -H 'Authorization: Bearer %s' https://%s.%s.svc.cluster.local:8443/metrics"],
							"securityContext": {
								"allowPrivilegeEscalation": false,
								"capabilities": {
									"drop": ["ALL"]
								},
								"runAsNonRoot": true,
								"runAsUser": 1000,
								"seccompProfile": {
									"type": "RuntimeDefault"
								}
							}
						}],
						"serviceAccount": "%s"
					}
				}`, token, metricsServiceName, namespace, serviceAccountName))
				Expect(utils.Run(cmd)).Error().NotTo(HaveOccurred(), "Failed to create pod", getPodName())

				By("waiting for the curl-metrics pod to complete.")
				verifyCurlUp := func(g Gomega) {
					cmd := exec.Command("kubectl", "get", "pods", GetCurlMetricsPodName(getPodName()),
						"-o", "jsonpath={.status.phase}",
						"-n", namespace)
					output, err := utils.Run(cmd)
					g.Expect(err).NotTo(HaveOccurred())
					g.Expect(output).To(Equal("Succeeded"), "curl pod in wrong status")
				}
				Eventually(verifyCurlUp, 5*time.Minute).Should(Succeed())

				By("getting the metrics by checking curl-metrics logs")
				metricsOutput := getMetricsOutput(GetCurlMetricsPodName(getPodName()))
				Expect(metricsOutput).To(ContainSubstring(metricsSubstring))
			},
			Entry(
				"controller pod",
				"tekton-kueue-controller-manager-metrics-service",
				"tekton-kueue-controller-manager",
				"controller_runtime_reconcile_total",
				testContext.GetControllerPodName,
			),
			Entry(
				"webhook pod",
				"tekton-kueue-webhook-service",
				"tekton-kueue-webhook",
				"controller_runtime_webhook_panics_total",
				testContext.GetWebhookPodName,
			),
		)

		It("should provisioned cert-manager", func() {
			By("validating that cert-manager has the certificate Secret")
			verifyCertManager := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "secrets", "webhook-server-cert", "-n", namespace)
				_, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
			}
			Eventually(verifyCertManager).Should(Succeed())
		})

		It("should have CA injection for mutating webhooks", func() {
			By("checking CA injection for mutating webhooks")
			verifyCAInjection := func(g Gomega) {
				cmd := exec.Command("kubectl", "get",
					"mutatingwebhookconfigurations.admissionregistration.k8s.io",
					"tekton-kueue-mutating-webhook-configuration",
					"-o", "go-template={{ range .webhooks }}{{ .clientConfig.caBundle }}{{ end }}")
				mwhOutput, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(len(mwhOutput)).To(BeNumerically(">", 10))
			}
			Eventually(verifyCAInjection).Should(Succeed())
		})

		// +kubebuilder:scaffold:e2e-webhooks-checks

		// TODO: Customize the e2e test suite with scenarios specific to your project.
		// Consider applying sample/CR(s) and check their status and/or verifying
		// the reconciliation by using the metrics, i.e.:
		// metricsOutput := getMetricsOutput()
		// Expect(metricsOutput).To(ContainSubstring(
		//    fmt.Sprintf(`controller_runtime_reconcile_total{controller="%s",result="success"} 1`,
		//    strings.ToLower(<Kind>),
		// ))
	})

	Context("N pipelines complete successfully", Ordered, func() {
		plrCount := 5
		plrs := make([]*tekv1.PipelineRun, plrCount)

		It("Starts PipelineRuns", func(ctx context.Context) {
			for i := range plrCount {
				plr := plrTemplate.DeepCopy()
				Eventually(
					func() error {
						return k8sClient.Create(ctx, plr)
					},
					90*time.Second,
					3*time.Second,
				).Should(Succeed())
				plrs[i] = plr
			}

		})

		It("A matching workload was created for each PipelineRun", func(ctx context.Context) {
			for i := range plrCount {
				plr := plrs[i]
				Eventually(func() error {
					wl, err := GetOwnedWorkload(k8sClient, plr, ctx)
					if err != nil {
						return err
					}
					const defaultPriorityClassName = "tekton-kueue-default"
					priorityName := ""
					if wl.Spec.PriorityClassRef != nil {
						priorityName = wl.Spec.PriorityClassRef.Name
					}
					if priorityName != defaultPriorityClassName {
						return fmt.Errorf(
							"Workload should have priority class %s, but has %s",
							defaultPriorityClassName,
							priorityName,
						)
					}
					return err
				},
					15*time.Second,
					3*time.Second,
				).Should(Succeed())
			}
		})

		It("PipelineRuns were completed Successfully", func(ctx context.Context) {
			for i := range plrCount {
				key := plrs[i].GetNamespacedName()
				plr := &tekv1.PipelineRun{}
				Eventually(func() error {
					err := k8sClient.Get(ctx, key, plr)
					if err != nil {
						return err
					}
					condition := plr.Status.GetCondition(kapi.ConditionSucceeded)
					if condition == nil {
						return fmt.Errorf("Success condition for PipelinerRun %s is nil", plr.Name)
					}
					success := (condition.Reason == tekv1.PipelineRunReasonSuccessful.String()) ||
						(condition.Reason == tekv1.PipelineRunReasonCompleted.String())
					if !success {
						return fmt.Errorf("PipelineRun %s didn't succeed", plr.Name)
					}
					return nil
				},
					(15*plrCount)*int(time.Second),
					3*time.Second,
				).Should(Succeed())
			}
		})
	})

	Context("Pipeline is queued when memory resources are missing", Ordered, func() {
		var plr *tekv1.PipelineRun
		It("PipelineRun is queued because lack of resources", func(ctx context.Context) {
			plr = plrTemplate.DeepCopy()
			plr.Annotations = map[string]string{
				"kueue.konflux-ci.dev/requests-memory": "2Gi",
			}
			Eventually(
				func() error {
					return k8sClient.Create(ctx, plr)
				},
				90*time.Second,
				3*time.Second,
			).Should(Succeed())
		})

		It("Large Pipelinerun is Pending", func(ctx context.Context) {
			EnsurePipelineRunSpecStatusIs(
				tekv1.PipelineRunSpecStatusPending,
				plr,
				k8sClient,
				ctx,
			)
		})

		It("A matching workload was created for the PipelineRun", func(ctx context.Context) {
			EnsureMatchingWorkloadExistWithStatusCondition(
				kueue.WorkloadQuotaReserved,
				metav1.ConditionFalse,
				"insufficient quota for memory",
				plr,
				k8sClient,
				ctx,
			)
		})
	})

	Context("PipelineRun is queued when the allowed number of PipelineRuns is 0", Ordered, func() {
		var plr *tekv1.PipelineRun
		It("PipelineRun is queued because lack of resources", func(ctx context.Context) {
			plr = plrTemplate.DeepCopy()
			plr.Labels = map[string]string{
				common.QueueLabel: "blocking-pipelines-queue",
			}
			Eventually(
				func() error {
					return k8sClient.Create(ctx, plr)
				},
				90*time.Second,
				3*time.Second,
			).Should(Succeed())
		})

		It("Pipelinerun is Pending", func(ctx context.Context) {
			EnsurePipelineRunSpecStatusIs(
				tekv1.PipelineRunSpecStatusPending,
				plr,
				k8sClient,
				ctx,
			)
		})

		It("A matching workload was created for the PipelineRun", func(ctx context.Context) {
			EnsureMatchingWorkloadExistWithStatusCondition(
				kueue.WorkloadQuotaReserved,
				metav1.ConditionFalse,
				"insufficient quota for tekton.dev/pipelineruns",
				plr,
				k8sClient,
				ctx,
			)
		})
	})

	Context("Reload Config when ConfigMap is updated", Ordered, Label("config", "smoke"), func() {
		queueName := "my-updated-queue"
		kueueConfig := config.Config{
			QueueName: queueName,
		}

		cfgMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      common.ConfigMapName,
				Namespace: "tekton-kueue",
			},
			Data: map[string]string{},
		}

		It("Updated Queue Label must be applied on pipelinerun", func(ctx context.Context) {

			cfgMapData, err := yaml.Marshal(kueueConfig)
			Expect(err).ToNot(HaveOccurred())

			cfgMap.Data[common.ConfigKey] = string(cfgMapData)

			err = k8sClient.Update(ctx, cfgMap)
			Expect(err).NotTo(HaveOccurred())

			// ConfigMap Reconciler should load the updated configMap
			// Updated Label must be applied on Pipelinerun
			Eventually(func() string {
				plr := plrTemplate.DeepCopy()
				err = k8sClient.Create(ctx, plr)
				Expect(err).NotTo(HaveOccurred())
				return plr.ObjectMeta.Labels[common.QueueLabel]
			}).WithTimeout(time.Duration(30) * time.Second).Should(Equal(queueName))

		})

	})

	Context("CEL managedBy sets Spec.ManagedBy", Ordered, Label("config", "smoke"), func() {
		managedByValue := "test-controller.example.io"
		kueueConfig := config.Config{
			QueueName: "local-queue",
			CEL: config.CEL{
				Expressions: []string{
					fmt.Sprintf(`managedBy("%s")`, managedByValue),
				},
			},
		}

		cfgMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      common.ConfigMapName,
				Namespace: "tekton-kueue",
			},
			Data: map[string]string{},
		}

		It("should set ManagedBy on created PipelineRun", func(ctx context.Context) {
			cfgMapData, err := yaml.Marshal(kueueConfig)
			Expect(err).ToNot(HaveOccurred())

			cfgMap.Data[common.ConfigKey] = string(cfgMapData)

			err = k8sClient.Update(ctx, cfgMap)
			Expect(err).NotTo(HaveOccurred())

			Eventually(func() *string {
				plr := plrTemplate.DeepCopy()
				err = k8sClient.Create(ctx, plr)
				Expect(err).NotTo(HaveOccurred())
				return plr.Spec.ManagedBy
			}).WithTimeout(time.Duration(30) * time.Second).Should(HaveValue(Equal(managedByValue)))
		})
	})

	Context("Ignore Config when ConfigMap is updated with invalid Values", Ordered, Label("config", "smoke"), func() {
		queueName := "my-ignored-queue"
		kueueConfig := config.Config{
			QueueName: queueName,
			CEL: config.CEL{
				Expressions: []string{
					"invalid-cel-expression",
				},
			},
		}

		cfgMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      common.ConfigMapName,
				Namespace: "tekton-kueue",
			},
			Data: map[string]string{},
		}

		It("Updated Queue Label must not be applied on pipelinerun", func(ctx context.Context) {

			cfgMapData, err := yaml.Marshal(kueueConfig)
			Expect(err).ToNot(HaveOccurred())

			cfgMap.Data[common.ConfigKey] = string(cfgMapData)

			err = k8sClient.Update(ctx, cfgMap)
			Expect(err).NotTo(HaveOccurred())

			// ConfigMap Reconciler should not load the updated configMap
			// Now label must not be applied on pipelinerun
			Consistently(func() string {
				plr := plrTemplate.DeepCopy()
				err = k8sClient.Create(ctx, plr)
				Expect(err).NotTo(HaveOccurred())

				// Updated Label must be applied on Pipelinerun
				return plr.ObjectMeta.Labels[common.QueueLabel]

			}).WithTimeout(time.Duration(10) * time.Second).
				WithPolling(500 * time.Millisecond).
				ShouldNot(Equal(queueName))

		})
	})

	Context("Invalid resource is requested", Ordered, func() {
		var plr *tekv1.PipelineRun
		It("rejects the PipelineRun", func(ctx context.Context) {
			plr = plrTemplate.DeepCopy()
			plr.Annotations = map[string]string{
				"kueue.konflux-ci.dev/requests-memory+invalid": "2Gi",
			}
			Expect(k8sClient.Create(ctx, plr)).Should(MatchError(kerrors.IsInvalid, "Invalid"))
		})
	})
})

func EnsureMatchingWorkloadExistWithStatusCondition(
	statusCondition string,
	expectedStatus metav1.ConditionStatus,
	expectedMessage string,
	plr *tekv1.PipelineRun,
	k8sClient client.Client,
	ctx context.Context,

) {
	Eventually(func(g Gomega) error {
		wl, err := GetOwnedWorkload(k8sClient, plr, ctx)
		g.Expect(err).ToNot(HaveOccurred())

		cond := apimeta.FindStatusCondition(wl.Status.Conditions, statusCondition)
		g.Expect(cond).ToNot(BeNil(), fmt.Sprintf("Didn't find %s condition for workload %s", statusCondition, wl.Name))

		g.Expect(cond.Status).To(
			Equal(expectedStatus),
			fmt.Sprintf("%s Condition status isn't %s", statusCondition, expectedStatus),
		)

		g.Expect(cond.Message).To(ContainSubstring(expectedMessage), "Didn't find expected condition message")

		return nil
	},
		15*time.Second,
		3*time.Second,
	).Should(Succeed())
}

func EnsurePipelineRunSpecStatusIs(
	status string,
	plr *tekv1.PipelineRun,
	k8sClient client.Client,
	ctx context.Context,
) {
	Eventually(
		func() error {
			key := plr.GetNamespacedName()
			err := k8sClient.Get(ctx, key, plr)
			if err != nil {
				return err
			}
			if plr.Spec.Status != tekv1.PipelineRunSpecStatusPending {
				return fmt.Errorf("PipelineRun status is %s and not Pending", plr.Spec.Status)
			}

			return nil
		},
		30*time.Second,
		3*time.Second,
	).Should(Succeed())
}

func GetOwnedWorkload(k8sClient client.Client, plr *tekv1.PipelineRun, ctx context.Context) (*kueue.Workload, error) {
	wlList := &kueue.WorkloadList{}
	err := k8sClient.List(
		ctx,
		wlList,
		client.InNamespace(plr.GetNamespace()),
		jobframework.OwnerReferenceIndexFieldMatcher(tekv1.SchemeGroupVersion.WithKind("PipelineRun"), plr.Name),
	)
	if err != nil {
		return nil, err
	}
	if len(wlList.Items) != 1 {
		return nil, fmt.Errorf("found %d workloads owned by PipelineRun %s", len(wlList.Items), plr.Name)
	}
	wl := wlList.Items[0]
	hasOwner, err := controllerutil.HasOwnerReference(wl.OwnerReferences, plr, k8sClient.Scheme())
	if err != nil {
		return nil, err
	}
	if !hasOwner {
		return nil, fmt.Errorf("The workload owner doesn't match the pipelinerun %s", plr.Name)
	}
	return &wl, nil
}

func getK8sClientOrDie(ctx context.Context) client.Client {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(tekv1.AddToScheme(scheme))
	utilruntime.Must(kueue.AddToScheme(scheme))

	cfg := ctrl.GetConfigOrDie()

	k8sCache, err := cache.New(cfg, cache.Options{Scheme: scheme, ReaderFailOnMissingInformer: true})
	Expect(err).ToNot(HaveOccurred(), "failed to create cache")

	_, err = k8sCache.GetInformer(ctx, &kueue.Workload{})
	Expect(err).ToNot(HaveOccurred(), "failed to setup informer for workloads")

	_, err = k8sCache.GetInformer(ctx, &tekv1.PipelineRun{})
	Expect(err).ToNot(HaveOccurred(), "failed to setup informer for pipelineruns")

	Expect(jobframework.SetupWorkloadOwnerIndex(
		ctx,
		k8sCache,
		tekv1.SchemeGroupVersion.WithKind("PipelineRun"),
	)).To(Succeed(), "failed to setup indexer")

	go func() {
		if err := k8sCache.Start(ctx); err != nil {
			panic(err)
		}
	}()

	if synced := k8sCache.WaitForCacheSync(ctx); !synced {
		panic("failed waiting for cache to sync")
	}

	k8sClient, err := client.New(
		cfg,
		client.Options{
			Cache:  &client.CacheOptions{Reader: k8sCache},
			Scheme: scheme,
		},
	)
	Expect(err).ToNot(HaveOccurred(), "failed to create client")

	return k8sClient
}

// serviceAccountToken returns a token for the specified service account in the given namespace.
// It uses the Kubernetes TokenRequest API to generate a token by directly sending a request
// and parsing the resulting token from the API response.
func serviceAccountToken(serviceAccountName string) (string, error) {
	const tokenRequestRawString = `{
		"apiVersion": "authentication.k8s.io/v1",
		"kind": "TokenRequest"
	}`

	// Temporary file to store the token request
	secretName := fmt.Sprintf("%s-token-request", serviceAccountName)
	tokenRequestFile := filepath.Join("/tmp", secretName)
	err := os.WriteFile(tokenRequestFile, []byte(tokenRequestRawString), os.FileMode(0o644))
	if err != nil {
		return "", err
	}

	var out string
	verifyTokenCreation := func(g Gomega) {
		// Execute kubectl command to create the token
		cmd := exec.Command("kubectl", "create", "--raw", fmt.Sprintf(
			"/api/v1/namespaces/%s/serviceaccounts/%s/token",
			namespace,
			serviceAccountName,
		), "-f", tokenRequestFile)

		output, err := cmd.CombinedOutput()
		g.Expect(err).NotTo(HaveOccurred())

		// Parse the JSON output to extract the token
		var token tokenRequest
		err = json.Unmarshal(output, &token)
		g.Expect(err).NotTo(HaveOccurred())

		out = token.Status.Token
	}
	Eventually(verifyTokenCreation).Should(Succeed())

	return out, err
}

// getMetricsOutput retrieves and returns the logs from the curl pod used to access the metrics endpoint.
func getMetricsOutput(podName string) string {
	By("getting the curl-metrics logs")
	cmd := exec.Command("kubectl", "logs", podName, "-n", namespace)
	metricsOutput, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred(), "Failed to retrieve logs from pod", podName)
	Expect(metricsOutput).To(ContainSubstring("< HTTP/1.1 200 OK"))
	return metricsOutput
}

// tokenRequest is a simplified representation of the Kubernetes TokenRequest API response,
// containing only the token field that we need to extract.
type tokenRequest struct {
	Status struct {
		Token string `json:"token"`
	} `json:"status"`
}
