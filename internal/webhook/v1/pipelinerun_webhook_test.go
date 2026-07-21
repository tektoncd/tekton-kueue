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

package v1

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/konflux-ci/tekton-kueue/internal/cel"
	"github.com/konflux-ci/tekton-kueue/pkg/common"
	"github.com/konflux-ci/tekton-kueue/pkg/config"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	tektondevv1 "github.com/tektoncd/pipeline/pkg/apis/pipeline/v1"
	admissionv1 "k8s.io/api/admission/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

func TestV1Webhook(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "V1 Webhook Suite")
}

var _ = Describe("PipelineRun Webhook", func() {
	var (
		defaulter webhook.CustomDefaulter
		plr       *tektondevv1.PipelineRun
	)

	BeforeEach(func(ctx context.Context) {
		plr = &tektondevv1.PipelineRun{
			Spec: tektondevv1.PipelineRunSpec{
				PipelineRef: &tektondevv1.PipelineRef{
					Name: "test-pipeline",
				},
			},
		}
	})

	Describe("Default", func() {
		Context("when MultiKueueOverride is true", func() {
			It("should set the managedBy", func(ctx context.Context) {
				cfg := &config.Config{
					QueueName:          "test-queue",
					MultiKueueOverride: true,
				}

				cfgStore := &ConfigStore{
					config: cfg,
				}

				var err error
				defaulter, err = NewCustomDefaulter(cfgStore)
				Expect(err).NotTo(HaveOccurred())
				err = defaulter.Default(ctx, plr)
				Expect(err).NotTo(HaveOccurred())
				Expect(*plr.Spec.ManagedBy).To(Equal(common.ManagedByMultiKueueLabel))
				Expect(plr.Spec.Status).To(Equal(tektondevv1.PipelineRunSpecStatus(tektondevv1.PipelineRunSpecStatusPending)))
			})
		})

		Context("when MultiKueueOverride is false", func() {
			It("should set the status to Pending", func(ctx context.Context) {
				cfg := &config.Config{
					QueueName:          "test-queue",
					MultiKueueOverride: false,
				}
				cfgStore := &ConfigStore{
					config: cfg,
				}
				var err error
				defaulter, err = NewCustomDefaulter(cfgStore)
				Expect(err).NotTo(HaveOccurred())
				err = defaulter.Default(ctx, plr)
				Expect(err).NotTo(HaveOccurred())
				Expect(plr.Spec.Status).To(Equal(tektondevv1.PipelineRunSpecStatus(tektondevv1.PipelineRunSpecStatusPending)))
			})
		})

		It("should set the queue name", func(ctx context.Context) {
			cfg := &config.Config{
				QueueName: "test-queue",
			}
			cfgStore := &ConfigStore{
				config: cfg,
			}
			var err error
			defaulter, err = NewCustomDefaulter(cfgStore)
			Expect(err).NotTo(HaveOccurred())
			err = defaulter.Default(ctx, plr)
			Expect(err).NotTo(HaveOccurred())
			Expect(plr.Labels[common.QueueLabel]).To(Equal("test-queue"))
		})

		It("should accept a valid PipelineRun with pipelineRef", func(ctx context.Context) {
			plrWithRef := &tektondevv1.PipelineRun{
				Spec: tektondevv1.PipelineRunSpec{
					PipelineRef: &tektondevv1.PipelineRef{
						Name: "my-pipeline",
					},
				},
			}

			cfg := &config.Config{
				QueueName: "test-queue",
			}
			cfgStore := &ConfigStore{
				config: cfg,
			}
			var err error
			defaulter, err = NewCustomDefaulter(cfgStore)
			Expect(err).NotTo(HaveOccurred())
			err = defaulter.Default(ctx, plrWithRef)
			Expect(err).NotTo(HaveOccurred())
			Expect(plrWithRef.Spec.Status).To(Equal(tektondevv1.PipelineRunSpecStatus(tektondevv1.PipelineRunSpecStatusPending)))
			Expect(plrWithRef.Labels[common.QueueLabel]).To(Equal("test-queue"))
		})

		It("should accept a valid PipelineRun with pipelineSpec", func(ctx context.Context) {
			plrWithSpec := &tektondevv1.PipelineRun{
				Spec: tektondevv1.PipelineRunSpec{
					PipelineSpec: &tektondevv1.PipelineSpec{
						Tasks: []tektondevv1.PipelineTask{
							{
								Name: "echo",
								TaskSpec: &tektondevv1.EmbeddedTask{
									TaskSpec: tektondevv1.TaskSpec{
										Steps: []tektondevv1.Step{
											{
												Name:   "echo",
												Image:  "alpine:latest",
												Script: "echo hello",
											},
										},
									},
								},
							},
						},
					},
				},
			}

			cfg := &config.Config{
				QueueName: "test-queue",
			}
			cfgStore := &ConfigStore{
				config: cfg,
			}
			var err error
			defaulter, err = NewCustomDefaulter(cfgStore)
			Expect(err).NotTo(HaveOccurred())
			err = defaulter.Default(ctx, plrWithSpec)
			Expect(err).NotTo(HaveOccurred())
			Expect(plrWithSpec.Spec.Status).To(Equal(tektondevv1.PipelineRunSpecStatus(tektondevv1.PipelineRunSpecStatusPending)))
			Expect(plrWithSpec.Labels[common.QueueLabel]).To(Equal("test-queue"))
		})

		It("should reject an invalid PipelineRun with neither pipelineRef nor pipelineSpec", func(ctx context.Context) {
			invalidPlr := &tektondevv1.PipelineRun{
				Spec: tektondevv1.PipelineRunSpec{
					// Neither pipelineRef nor pipelineSpec is set - this is invalid
				},
			}

			programs, err := cel.CompileCELPrograms([]string{`label("env", "test")`})
			Expect(err).NotTo(HaveOccurred())

			cfg := &config.Config{
				QueueName: "test-queue",
			}
			cfgStore := &ConfigStore{
				config: cfg,
				mutators: []PipelineRunMutator{
					cel.NewCELMutator(programs),
				},
			}
			defaulter, err = NewCustomDefaulter(cfgStore)
			Expect(err).NotTo(HaveOccurred())
			Expect(defaulter.Default(ctx, invalidPlr)).
				Error().
				To(And(
					Satisfy(errors.IsBadRequest),
					MatchError(ContainSubstring("invalid pipelinerun: expected exactly one, got neither: pipelineRef, pipelineSpec"))))
		})

		It("should accept a PipelineRun with pipelineSpec containing a parameter without explicit type", func(ctx context.Context) {
			// This reproduces the bug where PipelineRuns with parameters missing the 'type' field
			// (like enable-cache-proxy) were rejected with:
			// "invalid value: : params.enable-cache-proxy.type"
			// The type should default to "string" and be valid.
			plrWithParamNoType := &tektondevv1.PipelineRun{
				Spec: tektondevv1.PipelineRunSpec{
					Params: []tektondevv1.Param{
						{
							Name:  "enable-cache-proxy",
							Value: tektondevv1.ParamValue{Type: tektondevv1.ParamTypeString, StringVal: "false"},
						},
					},
					PipelineSpec: &tektondevv1.PipelineSpec{
						Params: []tektondevv1.ParamSpec{
							{
								Name:        "enable-cache-proxy",
								Description: "Enable cache proxy configuration",
								Default:     &tektondevv1.ParamValue{Type: tektondevv1.ParamTypeString, StringVal: "false"},
								// Note: Type field is NOT set - this should default to string
							},
						},
						Tasks: []tektondevv1.PipelineTask{
							{
								Name: "init",
								Params: []tektondevv1.Param{
									{
										Name:  "enable-cache-proxy",
										Value: tektondevv1.ParamValue{Type: tektondevv1.ParamTypeString, StringVal: "$(params.enable-cache-proxy)"},
									},
								},
								TaskSpec: &tektondevv1.EmbeddedTask{
									TaskSpec: tektondevv1.TaskSpec{
										Params: []tektondevv1.ParamSpec{
											{
												Name: "enable-cache-proxy",
												// Type field is NOT set
											},
										},
										Steps: []tektondevv1.Step{
											{
												Name:   "echo",
												Image:  "alpine:latest",
												Script: "echo $(params.enable-cache-proxy)",
											},
										},
									},
								},
							},
						},
					},
				},
			}

			cfg := &config.Config{
				QueueName: "test-queue",
			}
			cfgStore := &ConfigStore{
				config: cfg,
			}
			var err error
			defaulter, err = NewCustomDefaulter(cfgStore)
			Expect(err).NotTo(HaveOccurred())
			err = defaulter.Default(ctx, plrWithParamNoType)
			Expect(err).NotTo(HaveOccurred())
			Expect(plrWithParamNoType.Spec.Status).To(Equal(tektondevv1.PipelineRunSpecStatus(tektondevv1.PipelineRunSpecStatusPending)))
			Expect(plrWithParamNoType.Labels[common.QueueLabel]).To(Equal("test-queue"))
		})

		It("should reject an invalid PipelineRun", func(ctx context.Context) {
			// found via fuzzing
			badJson := []byte("{\"spec\":{\"pipelineSpec\":{\"params\":[{}]},\"params\":[{}]}}")

			pipelineRun := tektondevv1.PipelineRun{}
			Expect(json.Unmarshal(badJson, &pipelineRun)).To(Succeed())

			programs, err := cel.CompileCELPrograms([]string{`label("env", "test")`})
			Expect(err).NotTo(HaveOccurred())

			cfg := &config.Config{
				QueueName: "test-queue",
			}
			cfgStore := &ConfigStore{
				config: cfg,
				mutators: []PipelineRunMutator{
					cel.NewCELMutator(programs),
				},
			}
			defaulter, err = NewCustomDefaulter(cfgStore)
			Expect(err).NotTo(HaveOccurred())
			// we expect to see a 400 Bad Request here
			Expect(defaulter.Default(ctx, &pipelineRun)).
				Error().
				To(And(
					Satisfy(errors.IsBadRequest),
					MatchError(ContainSubstring("pipelinerun validation failed"))))
		})

		It("should return InternalServerError when CEL evaluation fails", func(ctx context.Context) {
			validPlr := &tektondevv1.PipelineRun{
				Spec: tektondevv1.PipelineRunSpec{
					PipelineRef: &tektondevv1.PipelineRef{
						Name: "my-pipeline",
					},
				},
			}

			// Use a CEL expression that accesses a non-existent field, causing a runtime error
			programs, err := cel.CompileCELPrograms([]string{`annotation("key", pipelineRun.doesNotExist)`})
			Expect(err).NotTo(HaveOccurred())

			cfgStore := &ConfigStore{
				config:   &config.Config{QueueName: "test-queue"},
				mutators: []PipelineRunMutator{cel.NewCELMutator(programs)},
			}
			defaulter, err = NewCustomDefaulter(cfgStore)
			Expect(err).NotTo(HaveOccurred())
			Expect(defaulter.Default(ctx, validPlr)).
				Error().
				To(And(
					Satisfy(errors.IsInternalError),
					MatchError(ContainSubstring("CEL evaluation failed"))))
		})

		It("CEL managedBy should override config-level multiKueueOverride", func(ctx context.Context) {
			programs, err := cel.CompileCELPrograms([]string{`managedBy("my-custom-controller")`})
			Expect(err).NotTo(HaveOccurred())

			cfg := &config.Config{
				QueueName:          "test-queue",
				MultiKueueOverride: true,
			}
			cfgStore := &ConfigStore{
				config:   cfg,
				mutators: []PipelineRunMutator{cel.NewCELMutator(programs)},
			}

			defaulter, err = NewCustomDefaulter(cfgStore)
			Expect(err).NotTo(HaveOccurred())
			err = defaulter.Default(ctx, plr)
			Expect(err).NotTo(HaveOccurred())
			Expect(*plr.Spec.ManagedBy).To(Equal("my-custom-controller"),
				"CEL managedBy() should override the config-level multiKueueOverride value")
		})

		It("should reject a non-pipelinerun object", func(ctx context.Context) {
			cfg := &config.Config{
				QueueName: "test-queue",
			}
			cfgStore := &ConfigStore{
				config: cfg,
			}
			var err error
			defaulter, err = NewCustomDefaulter(cfgStore)
			Expect(err).NotTo(HaveOccurred())
			// we don't expect to see this in practice, but better safe than sorry
			Expect(defaulter.Default(ctx, &tektondevv1.Pipeline{})).
				Error().
				To(And(
					Satisfy(errors.IsBadRequest),
					MatchError(ContainSubstring("expected a PipelineRun object but got *v1.Pipeline"))))
		})
	})
})

// minimalPipelineRunJSON is a raw admission request as it would arrive from
// kubectl — no taskRunTemplate, no serviceAccountName, no status sub-resource.
var minimalPipelineRunJSON = []byte(`{
	"apiVersion": "tekton.dev/v1",
	"kind": "PipelineRun",
	"metadata": {
		"name": "test-plr",
		"namespace": "default"
	},
	"spec": {
		"pipelineRef": {"name": "test-pipeline"}
	}
}`)

// prePopulatedPipelineRunJSON is a raw  admission request. It has all the fields prepopulated
// Webhook is not supposed apply any patch here
var prePopulatedPipelineRunJSON = []byte(`{
	"apiVersion": "tekton.dev/v1",
	"kind": "PipelineRun",
	"metadata": {
		"name": "test-plr",
		"namespace": "default",
		"labels" : {
			"kueue.x-k8s.io/queue-name": "test-queue"
		}
	},
	"spec": {
		"status" : "PipelineRunPending",
		"pipelineRef": {"name": "test-pipeline"}
	}
}`)

// fieldsWeNeverTouch lists spec/status fields the webhook should never patch.
var fieldsWeNeverTouch = []string{
	"taskRunTemplate",
	"serviceAccountName",
	"taskRunSpecs",
	"workspaces",
	"timeouts",
}

func makeAdmissionRequest(raw []byte) admission.Request {
	return admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			Object:    k8sruntime.RawExtension{Raw: raw},
			Operation: admissionv1.Create,
		},
	}
}

var _ = Describe("Zero-value field leak (issue #319)", func() {
	var (
		cfgStore *ConfigStore
		scheme   *k8sruntime.Scheme
	)

	BeforeEach(func() {
		cfgStore = &ConfigStore{config: &config.Config{QueueName: "test-queue"}}
		scheme = k8sruntime.NewScheme()
		Expect(tektondevv1.AddToScheme(scheme)).To(Succeed())
	})

	It("raw CustomDefaulter leaks zero-value struct fields into patches", func(ctx context.Context) {

		defaulter, err := NewCustomDefaulter(cfgStore)
		Expect(err).NotTo(HaveOccurred())
		unfiltered := admission.WithCustomDefaulter(scheme, &tektondevv1.PipelineRun{}, defaulter)
		resp := unfiltered.Handle(ctx, makeAdmissionRequest(minimalPipelineRunJSON))
		Expect(resp.Allowed).To(BeTrue())

		_, _ = fmt.Fprintf(GinkgoWriter, "\n=== Unfiltered patches (%d) ===\n", len(resp.Patches))
		for i, p := range resp.Patches {
			_, _ = fmt.Fprintf(GinkgoWriter, "  [%d] op=%-7s path=%s\n", i, p.Operation, p.Path)
		}

		leaked := false
		for _, p := range resp.Patches {
			for _, field := range fieldsWeNeverTouch {
				if strings.Contains(p.Path, field) {
					leaked = true
				}
			}
		}
		Expect(leaked).To(BeTrue(),
			"expected the raw CustomDefaulter to leak zero-value fields — "+
				"if this passes, Go's json.Marshal omitempty behavior may have changed")
	})

	It("patchFilteringWebhook strips the leaked fields", func(ctx context.Context) {
		defaulter, err := NewCustomDefaulter(cfgStore)
		Expect(err).NotTo(HaveOccurred())

		inner := admission.WithCustomDefaulter(scheme, &tektondevv1.PipelineRun{}, defaulter)
		filtered := &patchFilteringWebhook{inner: inner}

		resp := filtered.Handle(ctx, makeAdmissionRequest(minimalPipelineRunJSON))
		Expect(resp.Allowed).To(BeTrue())
		Expect(resp.Patches).NotTo(BeEmpty())

		_, _ = fmt.Fprintf(GinkgoWriter, "\n=== Filtered patches (%d) ===\n", len(resp.Patches))
		for i, p := range resp.Patches {
			_, _ = fmt.Fprintf(GinkgoWriter, "  [%d] op=%-7s path=%s\n", i, p.Operation, p.Path)
		}

		for _, p := range resp.Patches {
			for _, field := range fieldsWeNeverTouch {
				Expect(p.Path).NotTo(ContainSubstring(field),
					fmt.Sprintf("patch at %s still contains '%s' after filtering", p.Path, field))
			}
		}
	})

	It("patchFilteringWebhook allows /spec/managedBy patches from CEL managedBy()", func(ctx context.Context) {
		programs, err := cel.CompileCELPrograms([]string{`managedBy("custom-controller.io")`})
		Expect(err).NotTo(HaveOccurred())

		cfgStore = &ConfigStore{
			config:   &config.Config{QueueName: "test-queue"},
			mutators: []PipelineRunMutator{cel.NewCELMutator(programs)},
		}

		defaulter, err := NewCustomDefaulter(cfgStore)
		Expect(err).NotTo(HaveOccurred())

		inner := admission.WithCustomDefaulter(scheme, &tektondevv1.PipelineRun{}, defaulter)
		filtered := &patchFilteringWebhook{inner: inner}

		resp := filtered.Handle(ctx, makeAdmissionRequest(minimalPipelineRunJSON))
		Expect(resp.Allowed).To(BeTrue())

		hasManagedByPatch := false
		for _, p := range resp.Patches {
			if strings.Contains(p.Path, "managedBy") {
				hasManagedByPatch = true
			}
		}
		Expect(hasManagedByPatch).To(BeTrue(),
			"expected /spec/managedBy patch to survive the patch filter")
	})

	It("patchFilteringWebhook sets Patch and PatchType to nil when there is nothing to patch", func(ctx context.Context) {
		defaulter, err := NewCustomDefaulter(cfgStore)
		Expect(err).NotTo(HaveOccurred())

		inner := admission.WithCustomDefaulter(scheme, &tektondevv1.PipelineRun{}, defaulter)
		filtered := &patchFilteringWebhook{inner: inner}

		resp := filtered.Handle(ctx, makeAdmissionRequest(prePopulatedPipelineRunJSON))

		Expect(resp.Allowed).To(BeTrue())
		Expect(resp.Patches).To(BeEmpty())
		Expect(resp.Patch).To(BeNil())
		Expect(resp.PatchType).To(BeNil())
	})
})
