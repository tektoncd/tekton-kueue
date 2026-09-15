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

package controller

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	tekv1 "github.com/tektoncd/pipeline/pkg/apis/pipeline/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
)

func newTestPipelineRun(opts ...func(*tekv1.PipelineRun)) *PipelineRun {
	plr := &tekv1.PipelineRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-plr",
			Namespace: "default",
		},
	}
	for _, opt := range opts {
		opt(plr)
	}
	return (*PipelineRun)(plr)
}

var _ = Describe("PipelineRun Controller", func() {
	Context("When reconciling a resource", func() {

		It("should successfully reconcile the resource", func() {

			// TODO(user): Add more specific assertions depending on your controller's reconciliation logic.
			// Example: If you expect a certain status condition after reconciliation, verify it here.
		})
	})

	Describe("resourcesRequests", func() {
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
	})

	Describe("PodSets", func() {
		It("should return an unretryable error when annotation has invalid resource quantity", func(ctx context.Context) {
			p := newTestPipelineRun(func(plr *tekv1.PipelineRun) {
				plr.Annotations = map[string]string{
					"kueue.konflux-ci.dev/requests-cpu": "not-a-quantity",
				}
			})
			Expect(p.PodSets(ctx)).Error().To(And(
				MatchError(ContainSubstring("invalid resource quantity")),
				Satisfy(jobframework.IsUnretryableError),
			))
		})
	})
})
