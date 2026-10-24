// Package controller implements the Kueue integration for Tekton PipelineRuns.
//
// It bridges Tekton and Kueue by wrapping PipelineRun as a Kueue GenericJob,
// allowing Kueue to manage PipelineRun scheduling, quota, and preemption.
// PipelineRuns are suspended (set to Pending) by the webhook on creation and
// only released by Kueue when cluster resources are available.
//
// Resource requirements for quota accounting are declared via annotations
// on the PipelineRun (e.g. kueue.konflux-ci.dev/requests-cpu). A synthetic
// "tekton.dev/pipelineruns" resource is always included to enable concurrency
// limits independent of actual compute resources.
package controller

import (
	"context"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"

	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/podset"

	tekv1 "github.com/tektoncd/pipeline/pkg/apis/pipeline/v1"
	corev1 "k8s.io/api/core/v1"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/utils/ptr"
	kapi "knative.dev/pkg/apis"

	kueueconfig "sigs.k8s.io/kueue/apis/config/v1beta2"
)

// +kubebuilder:rbac:groups=scheduling.k8s.io,resources=priorityclasses,verbs=list;get;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;watch;update;patch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/finalizers,verbs=update
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=resourceflavors,verbs=get;list;watch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloadpriorityclasses,verbs=get;list;watch
// +kubebuilder:rbac:groups="tekton.dev",resources=pipelineruns,verbs=watch;update;patch;list
// +kubebuilder:rbac:groups="tekton.dev",resources=pipelineruns/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=list;watch

// PipelineRun wraps tekv1.PipelineRun to implement Kueue's GenericJob and
// JobWithCustomStop interfaces. This is a type definition (not an alias) so
// we can attach methods without modifying the upstream Tekton type.
type PipelineRun tekv1.PipelineRun

const (
	ConditionTypeTerminationTarget = "TerminationTarget"
)

const (
	ControllerName = "KueuePipelineRunController"

	// ResourcePipelineRunCount is a synthetic resource name used in Kueue
	// workloads to track the number of concurrent PipelineRuns. Every
	// PipelineRun requests exactly 1 unit of this resource, enabling
	// ClusterQueue quotas to limit concurrency (e.g. "max 10 PipelineRuns").
	ResourcePipelineRunCount = "tekton.dev/pipelineruns"
)

const (
	annotationDomain            = "kueue.konflux-ci.dev/"
	annotationResourcesRequests = annotationDomain + "requests-"
)

var (
	_      jobframework.GenericJob        = &PipelineRun{}
	_      jobframework.JobWithCustomStop = &PipelineRun{}
	PLRGVK                                = tekv1.SchemeGroupVersion.WithKind("PipelineRun")
	PLRLog                                = ctrl.Log.WithName(ControllerName)
)

// SetupWithManager registers the PipelineRun reconciler with the manager using
// Kueue's generic reconciler factory. The factory handles Workload lifecycle
// (create, admit, suspend, evict) so this controller only needs to implement
// the GenericJob interface methods that map PipelineRun semantics to Kueue.
func SetupWithManager(ctx context.Context, mgr ctrl.Manager) error {
	reconcilerFactory := jobframework.NewGenericReconcilerFactory(
		func() jobframework.GenericJob { return &PipelineRun{} },
		func(b *builder.Builder, c client.Client) *builder.Builder {
			return b.Named("PipelineRunWorkloads")
		},
	)

	reconciler, err := reconcilerFactory(
		ctx,
		mgr.GetClient(),
		mgr.GetFieldIndexer(),
		mgr.GetEventRecorder("kueue-plr"),
		// In v1beta2, WaitForPodsReady removed its Enable bool field — a non-nil
		// pointer now means the feature is enabled. Pass nil to keep it disabled,
		// matching the original v1beta1 behavior where Enable defaulted to false.
		jobframework.WithWaitForPodsReady((*kueueconfig.WaitForPodsReady)(nil)),
	)
	if err != nil {
		return err
	}

	return reconciler.SetupWithManager(mgr)
}

// SetupIndexer creates the field index that Kueue uses to look up Workloads
// by their owner PipelineRun. This must be called before the reconciler starts.
func SetupIndexer(ctx context.Context, fieldIndexer client.FieldIndexer) error {
	return jobframework.SetupWorkloadOwnerIndex(ctx, fieldIndexer, tekv1.SchemeGroupVersion.WithKind("PipelineRun"))
}

// Stop implements jobframework.JobWithCustomStop.
// It gracefully stops a PipelineRun by setting its status to StoppedRunFinally,
// which tells Tekton to finish currently running tasks but not start new ones.
// Returns false if the PipelineRun is already done or in a terminal state.
func (p *PipelineRun) Stop(ctx context.Context, c client.Client, _ []podset.PodSetInfo, stopReason jobframework.StopReason, eventMsg string) (bool, error) {
	plr := (*tekv1.PipelineRun)(p)
	plrPendingOrRunning := (plr.Spec.Status == "") || (plr.Spec.Status == tekv1.PipelineRunSpecStatusPending)

	if plr.IsDone() || !plrPendingOrRunning {
		return false, nil
	}

	plrCopy := plr.DeepCopy()
	plrCopy.SetManagedFields(nil)
	plrCopy.Spec.Status = tekv1.PipelineRunSpecStatusStoppedRunFinally
	err := c.Patch(ctx, plrCopy, client.Apply, client.FieldOwner(ControllerName), client.ForceOwnership)
	if err != nil {
		return false, err
	}

	return true, nil
}

// Finished implements jobframework.GenericJob.
func (p *PipelineRun) Finished(_ context.Context) (message string, success bool, finished bool) {
	plr := (*tekv1.PipelineRun)(p)
	condition := plr.Status.GetCondition(kapi.ConditionSucceeded)

	if condition == nil {
		return "", false, false
	}

	message = condition.Message
	success = (condition.Reason == tekv1.PipelineRunReasonSuccessful.String()) ||
		(condition.Reason == tekv1.PipelineRunReasonCompleted.String())
	finished = plr.IsDone()

	return
}

// GVK implements jobframework.GenericJob.
func (p *PipelineRun) GVK() schema.GroupVersionKind {
	return PLRGVK
}

// IsActive implements jobframework.GenericJob.
func (p *PipelineRun) IsActive() bool {
	return (*tekv1.PipelineRun)(p).HasStarted()
}

// IsSuspended implements jobframework.GenericJob.
func (p *PipelineRun) IsSuspended() bool {
	return p.Spec.Status == tekv1.PipelineRunSpecStatusPending
}

// Object implements jobframework.GenericJob.
func (p *PipelineRun) Object() client.Object {
	return (*tekv1.PipelineRun)(p)
}

// PodSets implements jobframework.GenericJob.
// Returns a single synthetic PodSet representing the PipelineRun's resource
// needs. Unlike batch Jobs, PipelineRuns don't declare their pods upfront,
// so we use a dummy container whose resource requests are derived from
// annotations on the PipelineRun. This allows Kueue to account for resources
// without needing to know the actual task pod specifications.
func (p *PipelineRun) PodSets(_ context.Context, _ client.Client) ([]kueue.PodSet, error) {
	requests, err := p.resourcesRequests()
	if err != nil {
		return nil, err
	}

	return []kueue.PodSet{
		{
			Name: "pod-set-1",
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "dummy",
							Image: "dummy",
							Resources: corev1.ResourceRequirements{
								Requests: requests,
							},
						},
					},
				},
			},
			Count: 1,
		},
	}, nil
}

// resourcesRequests will match all annotations starting with
// `kueue.konflux-ci.dev/requests-`. Valid annotations to set
// the requested resources are then:
// * `kueue.konflux-ci.dev/requests-cpu`
// * `kueue.konflux-ci.dev/requests-memory`
// * `kueue.konflux-ci.dev/requests-storage`
// * `kueue.konflux-ci.dev/requests-ephemeral-storage`
//
// By default, a resource which indicates that the workload requires 1
// PipelineRun will be added. This is useful for controlling the number
// of PipelineRuns that can be executed concurrently.
func (p *PipelineRun) resourcesRequests() (corev1.ResourceList, error) {
	requests := corev1.ResourceList{
		ResourcePipelineRunCount: resource.MustParse("1"),
	}

	for k, v := range p.GetAnnotations() {
		n, q, err := p.parseResourcesRequestsAnnotation(k, v)
		switch {
		case err != nil:
			return nil, err
		case n != nil && q != nil:
			requests[*n] = *q
		}
	}

	return requests, nil
}

// parseResourcesRequestsAnnotation checks if an annotation is a ResourcesRequests one.
// It validates the extracted key and value. If the annotation is invalid the PipelineRun can not
// be correctly processed and it needs to be fixed. To avoid a reconciliation loop an
// UnretryableError is returned. This will tell Kueue's reconciler to avoid reconciling the
// PipelineRun at current state again. If a new event on the PipelineRun occurs, a new
// reconciliation will start.
func (p *PipelineRun) parseResourcesRequestsAnnotation(k, v string) (*corev1.ResourceName, *resource.Quantity, error) {
	t, ok := strings.CutPrefix(k, annotationResourcesRequests)
	if !ok {
		return nil, nil, nil
	}

	if t == "" {
		return nil, nil, jobframework.UnretryableError(
			fmt.Sprintf("empty resource name in annotation %s", k))
	}

	if corev1.ResourceName(t) == ResourcePipelineRunCount {
		return nil, nil, jobframework.UnretryableError(
			fmt.Sprintf("overriding the concurrency token %q via annotation is not allowed", ResourcePipelineRunCount))
	}

	q, err := resource.ParseQuantity(v)
	if err != nil {
		return nil, nil, jobframework.UnretryableError(
			fmt.Sprintf("invalid resource quantity in annotation %s=%q: %v", k, v, err))
	}

	if q.Sign() < 0 {
		return nil, nil, jobframework.UnretryableError(
			fmt.Sprintf("negative resource quantity in annotation %s=%q is not allowed", k, v))
	}

	return ptr.To(corev1.ResourceName(t)), &q, nil
}

// PodsReady implements jobframework.GenericJob.
// This method is never called because the WaitForPodsReady configuration is
// not enabled for PipelineRuns. Kueue tracks pod readiness for batch Jobs,
// but PipelineRuns manage their own pod lifecycle through Tekton.
func (p *PipelineRun) PodsReady(_ context.Context, _ client.Client) bool {
	panic("pods ready shouldn't be called")
}

// RestorePodSetsInfo implements jobframework.GenericJob.
func (p *PipelineRun) RestorePodSetsInfo(podSetsInfo []podset.PodSetInfo) bool {
	return false
}

// RunWithPodSetsInfo implements jobframework.GenericJob.
func (p *PipelineRun) RunWithPodSetsInfo(_ context.Context, _ client.Client, podSetsInfo []podset.PodSetInfo) error {
	p.Spec.Status = ""
	return nil
}

// Suspend implements jobframework.GenericJob.
func (p *PipelineRun) Suspend() {
	// Not implemented because this is not called when JobWithCustomStop is implemented.
}
