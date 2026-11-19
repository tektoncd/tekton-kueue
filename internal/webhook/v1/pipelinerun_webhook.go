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
	"errors"
	"fmt"
	"strings"

	"github.com/go-logr/logr"
	"github.com/konflux-ci/tekton-kueue/internal/cel"
	"github.com/konflux-ci/tekton-kueue/pkg/common"
	tekv1 "github.com/tektoncd/pipeline/pkg/apis/pipeline/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// SetupPipelineRunWebhookWithManager registers the webhook for PipelineRun in the manager.
// It wraps the standard CustomDefaulter handler with a patch filter to prevent
// controller-runtime's struct round-tripping from leaking zero-value fields
// (e.g. taskRunTemplate: {}) into the admission response. Such leaks block
// downstream webhooks (Tekton's) from applying their own defaults.
// See https://github.com/konflux-ci/tekton-kueue/issues/319
func SetupPipelineRunWebhookWithManager(mgr ctrl.Manager, defaulter admission.Defaulter[runtime.Object]) error {
	inner := admission.WithCustomDefaulter(mgr.GetScheme(), &tekv1.PipelineRun{}, defaulter)
	handler := &patchFilteringWebhook{inner: inner}
	mgr.GetWebhookServer().Register(
		"/mutate-tekton-dev-v1-pipelinerun",
		&admission.Webhook{Handler: handler, LogConstructor: logConstructor},
	)
	return nil
}

// allowedPatchPrefixes lists the JSON Pointer prefixes for fields that the
// webhook intentionally modifies. Any patch outside this allowlist is a
// side-effect of Go struct round-tripping and gets dropped.
var allowedPatchPrefixes = []string{
	"/metadata/labels",
	"/metadata/annotations",
	"/spec/status",
	"/spec/managedBy",
}

// patchFilteringWebhook wraps an admission.Handler and strips JSON patches
// that target fields the webhook never intends to modify.
type patchFilteringWebhook struct {
	inner admission.Handler
}

func (w *patchFilteringWebhook) Handle(ctx context.Context, req admission.Request) admission.Response {
	resp := w.inner.Handle(ctx, req)
	if len(resp.Patches) == 0 {
		return resp
	}

	n := 0
	for _, p := range resp.Patches {
		if isPatchAllowed(p.Path) {
			resp.Patches[n] = p
			n++
		}
	}
	resp.Patches = resp.Patches[:n]
	if n == 0 {
		resp.PatchType = nil
		resp.Patch = nil
	}
	return resp
}

func isPatchAllowed(path string) bool {
	for _, prefix := range allowedPatchPrefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

func logConstructor(base logr.Logger, req *admission.Request) logr.Logger {
	gvk := (&tekv1.PipelineRun{}).GetGroupVersionKind()
	log := base.WithValues(
		"webhookGroup", gvk.Group,
		"webhookKind", gvk.Kind,
	)
	if req != nil {
		log = log.WithValues(
			"webhookGroup", tekv1.SchemeGroupVersion.Group,
			"webhookKind", gvk.Kind,
			gvk.Kind, klog.KRef(req.Namespace, req.Name),
			"namespace", req.Namespace,
			"name", req.Name,
			"resource", req.Resource,
			"user", req.UserInfo.Username,
			"requestID", req.UID,
		)

		if a, err := meta.Accessor(req.Object); err == nil {
			if a.GetName() == "" {
				// add the generate name only if the name is unset
				return log.WithValues("generateName", a.GetGenerateName())
			}
		}
	}
	return log
}

// +kubebuilder:webhook:path=/mutate-tekton-dev-v1-pipelinerun,mutating=true,failurePolicy=fail,sideEffects=None,groups=tekton.dev,resources=pipelineruns,verbs=create,versions=v1,name=pipelinerun-kueue-defaulter.tekton-kueue.io,admissionReviewVersions=v1

// pipelineRunCustomDefaulter is the webhook handler that intercepts PipelineRun
// creation requests. It suspends each PipelineRun (Pending), assigns it to a
// Kueue queue, and applies any configured CEL mutations before the PipelineRun
// is persisted. The webhook uses failurePolicy=fail to prevent unqueued
// PipelineRuns from bypassing Kueue.
type pipelineRunCustomDefaulter struct {
	configStore *ConfigStore
}

func NewCustomDefaulter(configStore *ConfigStore) (admission.Defaulter[runtime.Object], error) {
	defaulter := &pipelineRunCustomDefaulter{
		configStore: configStore,
	}
	return defaulter, nil
}

// Default implements admission.Defaulter so a webhook will be registered for the Kind PipelineRun.
func (d *pipelineRunCustomDefaulter) Default(ctx context.Context, obj runtime.Object) error {
	plr, ok := obj.(*tekv1.PipelineRun)

	if !ok {
		return k8serrors.NewBadRequest(fmt.Sprintf("expected a PipelineRun object but got %T", obj))
	}

	plr.Spec.Status = tekv1.PipelineRunSpecStatusPending
	if plr.Labels == nil {
		plr.Labels = make(map[string]string)
	}
	config, mutators := d.configStore.GetConfigAndMutators()
	if _, exists := plr.Labels[common.QueueLabel]; !exists {
		plr.Labels[common.QueueLabel] = config.QueueName
	}
	if config.MultiKueueOverride {
		plr.Spec.ManagedBy = ptr.To(common.ManagedByMultiKueueLabel)
	}
	for _, mutator := range mutators {
		if err := mutator.Mutate(plr); err != nil {
			var validationErr *cel.ValidationError
			if errors.As(err, &validationErr) {
				return k8serrors.NewBadRequest(validationErr.Error())
			}
			var evaluationErr *cel.EvaluationError
			if errors.As(err, &evaluationErr) {
				return k8serrors.NewInternalError(evaluationErr)
			}
			return err
		}
	}

	return nil
}
