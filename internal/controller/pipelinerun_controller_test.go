/*
Copyright 2026.

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

package controller

import (
	"context"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	tekv1 "github.com/tektoncd/pipeline/pkg/apis/pipeline/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kapi "knative.dev/pkg/apis"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/podset"
)

func newTestPipelineRun(opts ...func(*tekv1.PipelineRun)) *PipelineRun {
	plr := &tekv1.PipelineRun{
		TypeMeta: metav1.TypeMeta{
			APIVersion: tekv1.SchemeGroupVersion.String(),
			Kind:       "PipelineRun",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-plr",
			Namespace: "default",
		},
	}
	for _, o := range opts {
		o(plr)
	}
	return (*PipelineRun)(plr)
}

var _ = Describe("PipelineRun", func() {

	Describe("GVK", func() {
		It("should return the PipelineRun GroupVersionKind", func() {
			p := newTestPipelineRun()
			gvk := p.GVK()
			Expect(gvk.Group).To(Equal(tekv1.SchemeGroupVersion.Group))
			Expect(gvk.Version).To(Equal(tekv1.SchemeGroupVersion.Version))
			Expect(gvk.Kind).To(Equal("PipelineRun"))
		})
	})

	Describe("IsActive", func() {
		It("should return false when PipelineRun has not started", func() {
			p := newTestPipelineRun()
			Expect(p.IsActive()).To(BeFalse())
		})

		It("should return true when PipelineRun has started", func() {
			now := metav1.Now()
			p := newTestPipelineRun(func(plr *tekv1.PipelineRun) {
				plr.Status.StartTime = &now
			})
			Expect(p.IsActive()).To(BeTrue())
		})
	})

	Describe("IsSuspended", func() {
		It("should return false when status is empty", func() {
			p := newTestPipelineRun()
			Expect(p.IsSuspended()).To(BeFalse())
		})

		It("should return true when status is Pending", func() {
			p := newTestPipelineRun(func(plr *tekv1.PipelineRun) {
				plr.Spec.Status = tekv1.PipelineRunSpecStatusPending
			})
			Expect(p.IsSuspended()).To(BeTrue())
		})

		It("should return false when status is StoppedRunFinally", func() {
			p := newTestPipelineRun(func(plr *tekv1.PipelineRun) {
				plr.Spec.Status = tekv1.PipelineRunSpecStatusStoppedRunFinally
			})
			Expect(p.IsSuspended()).To(BeFalse())
		})
	})

	Describe("Object", func() {
		It("should return the underlying PipelineRun as client.Object", func() {
			p := newTestPipelineRun()
			obj := p.Object()
			Expect(obj).NotTo(BeNil())
			Expect(obj.GetName()).To(Equal("test-plr"))
			Expect(obj.GetNamespace()).To(Equal("default"))
		})
	})

	Describe("PodsReady", func() {
		It("should panic because it should not be called", func(ctx context.Context) {
			p := newTestPipelineRun()
			Expect(func() { p.PodsReady(ctx, nil) }).To(PanicWith("pods ready shouldn't be called"))
		})
	})

	Describe("RestorePodSetsInfo", func() {
		It("should return false with nil input", func() {
			p := newTestPipelineRun()
			Expect(p.RestorePodSetsInfo(nil)).To(BeFalse())
		})

		It("should return false with non-empty input", func() {
			p := newTestPipelineRun()
			Expect(p.RestorePodSetsInfo([]podset.PodSetInfo{{}})).To(BeFalse())
		})
	})

	DescribeTable("RunWithPodSetsInfo should clear Spec.Status regardless of prior value",
		func(ctx context.Context, initialStatus string) {
			p := newTestPipelineRun(func(plr *tekv1.PipelineRun) {
				plr.Spec.Status = tekv1.PipelineRunSpecStatus(initialStatus)
			})
			Expect(p.RunWithPodSetsInfo(ctx, nil, nil)).To(Succeed())
			Expect(p.Spec.Status).To(BeEmpty())
		},
		Entry("when status is empty", ""),
		Entry("when status is Pending", string(tekv1.PipelineRunSpecStatusPending)),
		Entry("when status is StoppedRunFinally", string(tekv1.PipelineRunSpecStatusStoppedRunFinally)),
	)

	Describe("Suspend", func() {
		It("should not change the PipelineRun state (no-op)", func() {
			p := newTestPipelineRun(func(plr *tekv1.PipelineRun) {
				plr.Spec.Status = ""
			})
			p.Suspend()
			// Suspend is a no-op because JobWithCustomStop is implemented
			Expect(p.Spec.Status).To(BeEmpty())
		})
	})

	Describe("Finished", func() {
		It("should return empty values when no condition is set", func(ctx context.Context) {
			p := newTestPipelineRun()
			msg, success, finished := p.Finished(ctx)
			Expect(msg).To(BeEmpty())
			Expect(success).To(BeFalse())
			Expect(finished).To(BeFalse())
		})

		It("should report success when reason is Successful", func(ctx context.Context) {
			p := newTestPipelineRun(func(plr *tekv1.PipelineRun) {
				plr.Status.Conditions = []kapi.Condition{
					{
						Type:    kapi.ConditionSucceeded,
						Status:  corev1.ConditionTrue,
						Reason:  tekv1.PipelineRunReasonSuccessful.String(),
						Message: "All tasks completed successfully",
					},
				}
			})
			msg, success, finished := p.Finished(ctx)
			Expect(msg).To(Equal("All tasks completed successfully"))
			Expect(success).To(BeTrue())
			Expect(finished).To(BeTrue())
		})

		It("should report success when reason is Completed", func(ctx context.Context) {
			p := newTestPipelineRun(func(plr *tekv1.PipelineRun) {
				plr.Status.Conditions = []kapi.Condition{
					{
						Type:    kapi.ConditionSucceeded,
						Status:  corev1.ConditionTrue,
						Reason:  tekv1.PipelineRunReasonCompleted.String(),
						Message: "Pipeline completed",
					},
				}
			})
			msg, success, finished := p.Finished(ctx)
			Expect(msg).To(Equal("Pipeline completed"))
			Expect(success).To(BeTrue())
			Expect(finished).To(BeTrue())
		})

		It("should report failure when PipelineRun has failed", func(ctx context.Context) {
			p := newTestPipelineRun(func(plr *tekv1.PipelineRun) {
				plr.Status.Conditions = []kapi.Condition{
					{
						Type:    kapi.ConditionSucceeded,
						Status:  corev1.ConditionFalse,
						Reason:  "Failed",
						Message: "Task my-task failed",
					},
				}
			})
			msg, success, finished := p.Finished(ctx)
			Expect(msg).To(Equal("Task my-task failed"))
			Expect(success).To(BeFalse())
			Expect(finished).To(BeTrue())
		})

		It("should report not finished when condition status is Unknown", func(ctx context.Context) {
			p := newTestPipelineRun(func(plr *tekv1.PipelineRun) {
				plr.Status.Conditions = []kapi.Condition{
					{
						Type:    kapi.ConditionSucceeded,
						Status:  corev1.ConditionUnknown,
						Reason:  "Running",
						Message: "Tasks are still running",
					},
				}
			})
			msg, success, finished := p.Finished(ctx)
			Expect(msg).To(Equal("Tasks are still running"))
			Expect(success).To(BeFalse())
			Expect(finished).To(BeFalse())
		})
	})

	Describe("resourcesRequests", func() {
		It("should return only the default pipelinerun count when no annotations are set", func() {
			p := newTestPipelineRun()
			requests, err := p.resourcesRequests()
			Expect(err).NotTo(HaveOccurred())
			Expect(requests).To(And(
				HaveLen(1),
				HaveKeyWithValue(
					corev1.ResourceName(ResourcePipelineRunCount),
					resource.MustParse("1"),
				),
			))
		})

		It("should ignore annotations that do not match the resource requests prefix", func() {
			p := newTestPipelineRun(func(plr *tekv1.PipelineRun) {
				plr.Annotations = map[string]string{
					"some-other-annotation":       "value",
					"kueue.konflux-ci.dev/other":  "value",
					"kueue.konflux-ci.dev/config": "value",
				}
			})
			requests, err := p.resourcesRequests()
			Expect(err).NotTo(HaveOccurred())
			Expect(requests).To(And(
				HaveLen(1),
				HaveKey(corev1.ResourceName(ResourcePipelineRunCount)),
			))
		})

		It("should parse cpu and memory resource annotations", func() {
			p := newTestPipelineRun(func(plr *tekv1.PipelineRun) {
				plr.Annotations = map[string]string{
					"kueue.konflux-ci.dev/requests-cpu":    "500m",
					"kueue.konflux-ci.dev/requests-memory": "256Mi",
				}
			})
			requests, err := p.resourcesRequests()
			Expect(err).NotTo(HaveOccurred())
			Expect(requests).To(And(
				HaveLen(3),
				HaveKeyWithValue(corev1.ResourceName("cpu"), resource.MustParse("500m")),
				HaveKeyWithValue(corev1.ResourceName("memory"), resource.MustParse("256Mi")),
				HaveKeyWithValue(corev1.ResourceName(ResourcePipelineRunCount), resource.MustParse("1")),
			))
		})

		It("should parse ephemeral-storage and storage annotations", func() {
			p := newTestPipelineRun(func(plr *tekv1.PipelineRun) {
				plr.Annotations = map[string]string{
					"kueue.konflux-ci.dev/requests-ephemeral-storage": "10Gi",
					"kueue.konflux-ci.dev/requests-storage":           "50Gi",
				}
			})
			requests, err := p.resourcesRequests()
			Expect(err).NotTo(HaveOccurred())
			Expect(requests).To(And(
				HaveLen(3),
				HaveKeyWithValue(corev1.ResourceName("ephemeral-storage"), resource.MustParse("10Gi")),
				HaveKeyWithValue(corev1.ResourceName("storage"), resource.MustParse("50Gi")),
			))
		})

		It("should include only matching annotations when mixed with unrelated ones", func() {
			p := newTestPipelineRun(func(plr *tekv1.PipelineRun) {
				plr.Annotations = map[string]string{
					"kueue.konflux-ci.dev/requests-cpu": "2",
					"unrelated/annotation":              "ignored",
					"kueue.konflux-ci.dev/queue-name":   "also-ignored",
				}
			})
			requests, err := p.resourcesRequests()
			Expect(err).NotTo(HaveOccurred())
			Expect(requests).To(And(
				HaveLen(2),
				HaveKeyWithValue(corev1.ResourceName("cpu"), resource.MustParse("2")),
				HaveKeyWithValue(corev1.ResourceName(ResourcePipelineRunCount), resource.MustParse("1")),
			))
		})

		It("should return unretryable error when annotation key has empty resource name", func() {
			p := newTestPipelineRun(func(plr *tekv1.PipelineRun) {
				plr.Annotations = map[string]string{
					"kueue.konflux-ci.dev/requests-": "500m",
				}
			})
			requests, err := p.resourcesRequests()
			Expect(err).To(And(
				MatchError(ContainSubstring("empty resource name")),
				Satisfy(jobframework.IsUnretryableError),
			))
			Expect(requests).To(BeNil())
		})

		It("should return unretryable error when annotation value is not a valid resource.Quantity", func() {
			p := newTestPipelineRun(func(plr *tekv1.PipelineRun) {
				plr.Annotations = map[string]string{
					"kueue.konflux-ci.dev/requests-cpu": "not-a-quantity",
				}
			})
			requests, err := p.resourcesRequests()
			Expect(err).To(And(
				MatchError(And(
					ContainSubstring("invalid resource quantity"),
					ContainSubstring("not-a-quantity"),
				)),
				Satisfy(jobframework.IsUnretryableError),
			))
			Expect(requests).To(BeNil())
		})

		It("should return unretryable error when attempting to override the concurrency token", func() {
			p := newTestPipelineRun(func(plr *tekv1.PipelineRun) {
				plr.Annotations = map[string]string{
					"kueue.konflux-ci.dev/requests-tekton.dev/pipelineruns": "0",
				}
			})
			requests, err := p.resourcesRequests()
			Expect(err).To(And(
				MatchError(ContainSubstring("overriding the concurrency token")),
				Satisfy(jobframework.IsUnretryableError),
			))
			Expect(requests).To(BeNil())
		})

		It("should return unretryable error when annotation value is negative", func() {
			p := newTestPipelineRun(func(plr *tekv1.PipelineRun) {
				plr.Annotations = map[string]string{
					"kueue.konflux-ci.dev/requests-cpu": "-1",
				}
			})
			requests, err := p.resourcesRequests()
			Expect(err).To(And(
				MatchError(ContainSubstring("negative resource quantity")),
				Satisfy(jobframework.IsUnretryableError),
			))
			Expect(requests).To(BeNil())
		})
	})

	Describe("PodSets", func() {
		It("should return a single pod set with the correct structure", func(ctx context.Context) {
			p := newTestPipelineRun()
			podSets, err := p.PodSets(ctx, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(podSets).To(HaveLen(1))

			ps := podSets[0]
			Expect(string(ps.Name)).To(Equal("pod-set-1"))
			Expect(ps.Count).To(Equal(int32(1)))

			containers := ps.Template.Spec.Containers
			Expect(containers).To(HaveLen(1))
			Expect(containers[0].Name).To(Equal("dummy"))
			Expect(containers[0].Image).To(Equal("dummy"))
		})

		It("should include resource requests from annotations in the pod set", func(ctx context.Context) {
			p := newTestPipelineRun(func(plr *tekv1.PipelineRun) {
				plr.Annotations = map[string]string{
					"kueue.konflux-ci.dev/requests-cpu":    "4",
					"kueue.konflux-ci.dev/requests-memory": "8Gi",
				}
			})
			podSets, err := p.PodSets(ctx, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(podSets).To(HaveLen(1))
			Expect(podSets[0].Template.Spec.Containers).To(HaveLen(1))

			requests := podSets[0].Template.Spec.Containers[0].Resources.Requests
			Expect(requests).To(And(
				HaveKeyWithValue(corev1.ResourceName("cpu"), resource.MustParse("4")),
				HaveKeyWithValue(corev1.ResourceName("memory"), resource.MustParse("8Gi")),
				HaveKeyWithValue(corev1.ResourceName(ResourcePipelineRunCount), resource.MustParse("1")),
			))
		})

		It("should return an unretryable error when annotation has invalid resource quantity", func(ctx context.Context) {
			p := newTestPipelineRun(func(plr *tekv1.PipelineRun) {
				plr.Annotations = map[string]string{
					"kueue.konflux-ci.dev/requests-cpu": "not-a-quantity",
				}
			})
			Expect(p.PodSets(ctx, nil)).Error().To(And(
				MatchError(ContainSubstring("invalid resource quantity")),
				Satisfy(jobframework.IsUnretryableError),
			))
		})
	})

	Describe("Stop", func() {
		var s *runtime.Scheme

		BeforeEach(func() {
			s = runtime.NewScheme()
			Expect(tekv1.AddToScheme(s)).To(Succeed())
		})

		It("should return false when PipelineRun is already done", func(ctx context.Context) {
			p := newTestPipelineRun(func(plr *tekv1.PipelineRun) {
				plr.Status.Conditions = []kapi.Condition{
					{
						Type:   kapi.ConditionSucceeded,
						Status: corev1.ConditionTrue,
					},
				}
			})

			fakeClient := fake.NewClientBuilder().WithScheme(s).Build()
			stopped, err := p.Stop(ctx, fakeClient, nil, jobframework.StopReasonWorkloadEvicted, "evicted")
			Expect(err).NotTo(HaveOccurred())
			Expect(stopped).To(BeFalse())
		})

		It("should return false when PipelineRun is done with failure", func(ctx context.Context) {
			p := newTestPipelineRun(func(plr *tekv1.PipelineRun) {
				plr.Status.Conditions = []kapi.Condition{
					{
						Type:   kapi.ConditionSucceeded,
						Status: corev1.ConditionFalse,
					},
				}
			})

			fakeClient := fake.NewClientBuilder().WithScheme(s).Build()
			stopped, err := p.Stop(ctx, fakeClient, nil, jobframework.StopReasonWorkloadEvicted, "evicted")
			Expect(err).NotTo(HaveOccurred())
			Expect(stopped).To(BeFalse())
		})

		It("should return false when PipelineRun status is not pending or running", func(ctx context.Context) {
			p := newTestPipelineRun(func(plr *tekv1.PipelineRun) {
				plr.Spec.Status = tekv1.PipelineRunSpecStatusStoppedRunFinally
			})

			fakeClient := fake.NewClientBuilder().WithScheme(s).Build()
			stopped, err := p.Stop(ctx, fakeClient, nil, jobframework.StopReasonWorkloadEvicted, "evicted")
			Expect(err).NotTo(HaveOccurred())
			Expect(stopped).To(BeFalse())
		})

		It("should stop a running PipelineRun with empty status", func(ctx context.Context) {
			p := newTestPipelineRun(func(plr *tekv1.PipelineRun) {
				plr.Spec.Status = ""
			})
			tekPlr := (*tekv1.PipelineRun)(p)

			fakeClient := fake.NewClientBuilder().
				WithScheme(s).
				WithObjects(tekPlr).
				Build()

			stopped, err := p.Stop(ctx, fakeClient, nil, jobframework.StopReasonWorkloadEvicted, "evicted")
			Expect(err).NotTo(HaveOccurred())
			Expect(stopped).To(BeTrue())

			var updated tekv1.PipelineRun
			Expect(fakeClient.Get(ctx, client.ObjectKeyFromObject(tekPlr), &updated)).To(Succeed())
			Expect(string(updated.Spec.Status)).To(Equal(string(tekv1.PipelineRunSpecStatusStoppedRunFinally)))
		})

		It("should stop a pending PipelineRun", func(ctx context.Context) {
			p := newTestPipelineRun(func(plr *tekv1.PipelineRun) {
				plr.Spec.Status = tekv1.PipelineRunSpecStatusPending
			})
			tekPlr := (*tekv1.PipelineRun)(p)

			fakeClient := fake.NewClientBuilder().
				WithScheme(s).
				WithObjects(tekPlr).
				Build()

			stopped, err := p.Stop(ctx, fakeClient, nil, jobframework.StopReasonWorkloadEvicted, "evicted")
			Expect(err).NotTo(HaveOccurred())
			Expect(stopped).To(BeTrue())

			var updated tekv1.PipelineRun
			Expect(fakeClient.Get(ctx, client.ObjectKeyFromObject(tekPlr), &updated)).To(Succeed())
			Expect(string(updated.Spec.Status)).To(Equal(string(tekv1.PipelineRunSpecStatusStoppedRunFinally)))
		})

		It("should return error when the patch fails", func(ctx context.Context) {
			p := newTestPipelineRun(func(plr *tekv1.PipelineRun) {
				plr.Spec.Status = ""
			})
			tekPlr := (*tekv1.PipelineRun)(p)

			patchErr := fmt.Errorf("server unavailable")
			fakeClient := fake.NewClientBuilder().
				WithScheme(s).
				WithObjects(tekPlr).
				WithInterceptorFuncs(interceptor.Funcs{
					Patch: func(_ context.Context, _ client.WithWatch, _ client.Object, _ client.Patch, _ ...client.PatchOption) error {
						return patchErr
					},
				}).
				Build()

			stopped, err := p.Stop(ctx, fakeClient, nil, jobframework.StopReasonWorkloadEvicted, "evicted")
			Expect(err).To(MatchError("server unavailable"))
			Expect(stopped).To(BeFalse())
		})
	})
})
