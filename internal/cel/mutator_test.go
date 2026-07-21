package cel

import (
	"errors"
	"maps"
	"testing"

	. "github.com/onsi/gomega"
	tekv1 "github.com/tektoncd/pipeline/pkg/apis/pipeline/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

// Common test constants to reduce duplication
const (
	complexPriorityExpression = `pacEventType == 'push' ? priority('konflux-post-merge-build') :
		pacEventType == 'pull_request' ? priority('konflux-pre-merge-build') :
		pacTestEventType == 'push' ? priority('konflux-post-merge-test') :
		pacTestEventType == 'pull_request' ? priority('konflux-pre-merge-test') :

		has(pipelineRun.metadata.labels) &&
		'appstudio.openshift.io/service' in pipelineRun.metadata.labels &&
		pipelineRun.metadata.labels['appstudio.openshift.io/service'] == 'release' &&
		'pipelines.appstudio.openshift.io/type' in pipelineRun.metadata.labels &&
		pipelineRun.metadata.labels['pipelines.appstudio.openshift.io/type'] == 'managed' ?
		priority('konflux-release') :

		has(pipelineRun.metadata.labels) &&
		'appstudio.openshift.io/service' in pipelineRun.metadata.labels &&
		pipelineRun.metadata.labels['appstudio.openshift.io/service'] == 'release' &&
		'pipelines.appstudio.openshift.io/type' in pipelineRun.metadata.labels &&
		pipelineRun.metadata.labels['pipelines.appstudio.openshift.io/type'] == 'tenant' ?
		priority('konflux-tenant-release') :

		plrNamespace == 'mintmaker' ? priority('konflux-dependency-update') :
		priority('konflux-default')`

	buildPlatformsExpression = `has(pipelineRun.spec.params) &&
		pipelineRun.spec.params.exists(p, p.name == 'build-platforms') ?
		pipelineRun.spec.params.filter(
		  p,
		  p.name == 'build-platforms')[0]
		.value.map(
		  p,
		  annotation("kueue.konflux-ci.dev/requests-" + replace(p, "/", "-"), "1")
		) : []`

	oldStylePlatformsExpression = `has(pipelineRun.spec.pipelineSpec) &&
		has(pipelineRun.spec.pipelineSpec.tasks) &&
		pipelineRun.spec.pipelineSpec.tasks.size() > 0 ?
		pipelineRun.spec.pipelineSpec.tasks.map(
		  task,
		  has(task.params) ? task.params.filter(p, p.name == 'PLATFORM') : []
		)
		.filter(p, p.size() > 0)
		.map(
		  p,
		  annotation("kueue.konflux-ci.dev/requests-" + replace(p[0].value, "/", "-"), "1")
		) : []`
)

// Common test data helpers
func getBuildPlatformsParams() []tekv1.Param {
	return []tekv1.Param{
		{
			Name: "build-platforms",
			Value: tekv1.ParamValue{
				Type: tekv1.ParamTypeArray,
				ArrayVal: []string{
					"linux/arm64",
					"linux/amd64",
					"linux/s390x",
					"linux/ppc64le",
				},
			},
		},
	}
}

func getBuildPlatformsParamsSmall() []tekv1.Param {
	return []tekv1.Param{
		{
			Name: "build-platforms",
			Value: tekv1.ParamValue{
				Type: tekv1.ParamTypeArray,
				ArrayVal: []string{
					"linux/arm64",
					"linux/amd64",
				},
			},
		},
	}
}

// Helper function to create pipeline tasks with PLATFORM parameters
func getPipelineTasksWithPlatforms() []tekv1.PipelineTask {
	return []tekv1.PipelineTask{
		{
			Name:    "build-arm64",
			TaskRef: &tekv1.TaskRef{Name: "build-task"},
			Params: []tekv1.Param{
				{
					Name:  "PLATFORM",
					Value: tekv1.ParamValue{Type: tekv1.ParamTypeString, StringVal: "linux/arm64"},
				},
			},
		},
		{
			Name:    "build-amd64",
			TaskRef: &tekv1.TaskRef{Name: "build-task"},
			Params: []tekv1.Param{
				{
					Name:  "PLATFORM",
					Value: tekv1.ParamValue{Type: tekv1.ParamTypeString, StringVal: "linux/amd64"},
				},
			},
		},
		{
			Name:    "build-s390x",
			TaskRef: &tekv1.TaskRef{Name: "build-task"},
			Params: []tekv1.Param{
				{
					Name:  "PLATFORM",
					Value: tekv1.ParamValue{Type: tekv1.ParamTypeString, StringVal: "linux/s390x"},
				},
			},
		},
		{
			Name:    "no-platform-task",
			TaskRef: &tekv1.TaskRef{Name: "other-task"},
			// No PLATFORM parameter
		},
	}
}

// Helper function to create pipeline tasks without PLATFORM parameters
func getPipelineTasksWithoutPlatforms() []tekv1.PipelineTask {
	return []tekv1.PipelineTask{
		{
			Name:    "setup",
			TaskRef: &tekv1.TaskRef{Name: "setup-task"},
			Params: []tekv1.Param{
				{
					Name:  "VERSION",
					Value: tekv1.ParamValue{Type: tekv1.ParamTypeString, StringVal: "1.0"},
				},
			},
		},
		{
			Name:    "cleanup",
			TaskRef: &tekv1.TaskRef{Name: "cleanup-task"},
			// No parameters
		},
	}
}

func TestNewCELMutator(t *testing.T) {
	g := NewWithT(t)

	programs, err := CompileCELPrograms([]string{
		`annotation("test-key", "test-value")`,
		`label("env", "production")`,
	})
	g.Expect(err).NotTo(HaveOccurred())

	mutator := NewCELMutator(programs)

	g.Expect(mutator).NotTo(BeNil())
	g.Expect(mutator.programs).To(HaveLen(2))
}

func TestCELMutator_Mutate(t *testing.T) {
	tests := []struct {
		name                string
		expressions         []string
		namespace           string // optional, defaults to "test-namespace"
		initialLabels       map[string]string
		initialAnnotations  map[string]string
		initialParams       []tekv1.Param       // optional, for testing parameter access
		pipelineSpec        *tekv1.PipelineSpec // optional, for testing pipeline spec scenarios
		expectedLabels      map[string]string
		expectedAnnotations map[string]string
		expectErr           bool
		expectedErrType     any // target for errors.As (e.g., new(*EvaluationError))
		errMsg              string
	}{
		{
			name: "single annotation mutation",
			expressions: []string{
				`annotation("tekton.dev/pipeline", "my-pipeline")`,
			},
			initialLabels:      nil,
			initialAnnotations: nil,
			expectedLabels:     nil,
			expectedAnnotations: map[string]string{
				"tekton.dev/pipeline": "my-pipeline",
			},
			expectErr: false,
		},
		{
			name: "single label mutation",
			expressions: []string{
				`label("environment", "production")`,
			},
			initialLabels:      nil,
			initialAnnotations: nil,
			expectedLabels: map[string]string{
				"environment": "production",
			},
			expectedAnnotations: nil,
			expectErr:           false,
		},
		{
			name: "multiple mutations from single expression",
			expressions: []string{
				`[annotation("owner", "team-a"), label("env", "prod")]`,
			},
			initialLabels:      nil,
			initialAnnotations: nil,
			expectedLabels: map[string]string{
				"env": "prod",
			},
			expectedAnnotations: map[string]string{
				"owner": "team-a",
			},
			expectErr: false,
		},
		{
			name: "multiple expressions",
			expressions: []string{
				`annotation("pipeline", "test-pipeline")`,
				`label("stage", "testing")`,
				`annotation("version", "1.0")`,
			},
			initialLabels:      nil,
			initialAnnotations: nil,
			expectedLabels: map[string]string{
				"stage": "testing",
			},
			expectedAnnotations: map[string]string{
				"pipeline": "test-pipeline",
				"version":  "1.0",
			},
			expectErr: false,
		},
		{
			name: "merge with existing labels and annotations",
			expressions: []string{
				`annotation("new-annotation", "new-value")`,
				`label("new-label", "new-value")`,
			},
			initialLabels: map[string]string{
				"existing-label": "existing-value",
			},
			initialAnnotations: map[string]string{
				"existing-annotation": "existing-value",
			},
			expectedLabels: map[string]string{
				"existing-label": "existing-value",
				"new-label":      "new-value",
			},
			expectedAnnotations: map[string]string{
				"existing-annotation": "existing-value",
				"new-annotation":      "new-value",
			},
			expectErr: false,
		},
		{
			name: "overwrite existing values",
			expressions: []string{
				`annotation("existing-annotation", "updated-value")`,
				`label("existing-label", "updated-value")`,
			},
			initialLabels: map[string]string{
				"existing-label": "old-value",
			},
			initialAnnotations: map[string]string{
				"existing-annotation": "old-value",
			},
			expectedLabels: map[string]string{
				"existing-label": "updated-value",
			},
			expectedAnnotations: map[string]string{
				"existing-annotation": "updated-value",
			},
			expectErr: false,
		},
		{
			name: "runtime error in expression",
			expressions: []string{
				`annotation("", "test-value")`, // empty key should cause error
			},
			initialLabels:      nil,
			initialAnnotations: nil,
			expectErr:          true,
			expectedErrType:    new(*EvaluationError),
			errMsg:             "annotation key cannot be empty",
		},
		{
			name: "reference pipelineRun name",
			expressions: []string{
				`annotation("pipeline-name", pipelineRun.metadata.name)`,
			},
			initialLabels:      nil,
			initialAnnotations: nil,
			expectedLabels:     nil,
			expectedAnnotations: map[string]string{
				"pipeline-name": "test-pipeline",
			},
			expectErr: false,
		},
		{
			name: "reference pipelineRun namespace",
			expressions: []string{
				`label("namespace", pipelineRun.metadata.namespace)`,
			},
			initialLabels:      nil,
			initialAnnotations: nil,
			expectedLabels: map[string]string{
				"namespace": "test-namespace",
			},
			expectedAnnotations: nil,
			expectErr:           false,
		},
		{
			name: "reference pipelineRef name",
			expressions: []string{
				`annotation("pipeline-ref", pipelineRun.spec.pipelineRef.name)`,
			},
			initialLabels:      nil,
			initialAnnotations: nil,
			expectedLabels:     nil,
			expectedAnnotations: map[string]string{
				"pipeline-ref": "test-pipeline",
			},
			expectErr: false,
		},
		{
			name: "combine pipelineRun fields",
			expressions: []string{
				`[
					annotation("full-name", pipelineRun.metadata.namespace + "/" + pipelineRun.metadata.name),
					label("pipeline-ref", pipelineRun.spec.pipelineRef.name)
				]`,
			},
			initialLabels:      nil,
			initialAnnotations: nil,
			expectedLabels: map[string]string{
				"pipeline-ref": "test-pipeline",
			},
			expectedAnnotations: map[string]string{
				"full-name": "test-namespace/test-pipeline",
			},
			expectErr: false,
		},
		{
			name: "conditional expression based on pipelineRun fields",
			expressions: []string{
				`annotation("environment", pipelineRun.metadata.namespace == "test-namespace" ? "testing" : "production")`,
			},
			initialLabels:      nil,
			initialAnnotations: nil,
			expectedLabels:     nil,
			expectedAnnotations: map[string]string{
				"environment": "testing",
			},
			expectErr: false,
		},
		{
			name: "reference existing label from pipelineRun",
			expressions: []string{
				`annotation("copied-label", pipelineRun.metadata.labels["existing-label"])`,
			},
			initialLabels: map[string]string{
				"existing-label": "label-value",
			},
			initialAnnotations: nil,
			expectedLabels: map[string]string{
				"existing-label": "label-value",
			},
			expectedAnnotations: map[string]string{
				"copied-label": "label-value",
			},
			expectErr: false,
		},
		{
			name: "priority function with static value",
			expressions: []string{
				`priority("high")`,
			},
			initialLabels:      nil,
			initialAnnotations: nil,
			expectedLabels: map[string]string{
				"kueue.x-k8s.io/priority-class": "high",
			},
			expectedAnnotations: nil,
			expectErr:           false,
		},
		{
			name: "priority function with dynamic value from pipelineRun",
			expressions: []string{
				`priority(pipelineRun.metadata.namespace == "production" ? "high" : "low")`,
			},
			initialLabels:      nil,
			initialAnnotations: nil,
			expectedLabels: map[string]string{
				"kueue.x-k8s.io/priority-class": "low",
			},
			expectedAnnotations: nil,
			expectErr:           false,
		},
		{
			name: "priority function combined with other mutations",
			expressions: []string{
				`[
					priority("medium"),
					annotation("priority-set-by", "cel-mutator"),
					label("queue", "default")
				]`,
			},
			initialLabels:      nil,
			initialAnnotations: nil,
			expectedLabels: map[string]string{
				"kueue.x-k8s.io/priority-class": "medium",
				"queue":                         "default",
			},
			expectedAnnotations: map[string]string{
				"priority-set-by": "cel-mutator",
			},
			expectErr: false,
		},

		{
			name: "accessing parameter with invalid name - should fail",
			expressions: []string{
				"annotation('test', pipelineRun.doesNotExist)",
			},
			initialLabels:       nil,
			initialAnnotations:  nil,
			initialParams:       nil, // No parameters - should return empty array
			expectedLabels:      nil,
			expectedAnnotations: nil,
			expectErr:           true,
			expectedErrType:     new(*EvaluationError),
		},
		// Config.yaml expression tests
		{
			name: "config.yaml build-platforms expression - with parameters",
			expressions: []string{
				buildPlatformsExpression,
			},
			initialLabels:      nil,
			initialAnnotations: nil,
			initialParams:      getBuildPlatformsParams(),
			expectedLabels:     nil,
			expectedAnnotations: map[string]string{
				"kueue.konflux-ci.dev/requests-linux-arm64":   "1",
				"kueue.konflux-ci.dev/requests-linux-amd64":   "1",
				"kueue.konflux-ci.dev/requests-linux-s390x":   "1",
				"kueue.konflux-ci.dev/requests-linux-ppc64le": "1",
			},
			expectErr: false,
		},
		{
			name: "config.yaml build-platforms expression - no parameters",
			expressions: []string{
				buildPlatformsExpression,
			},
			initialLabels:       nil,
			initialAnnotations:  nil,
			initialParams:       nil,
			expectedLabels:      nil,
			expectedAnnotations: nil,
			expectErr:           false,
		},
		{
			name: "config.yaml old-style platforms expression - with pipeline spec",
			expressions: []string{
				oldStylePlatformsExpression,
			},
			initialLabels:      nil,
			initialAnnotations: nil,
			initialParams:      nil,
			pipelineSpec: &tekv1.PipelineSpec{
				Tasks: getPipelineTasksWithPlatforms(),
			},
			expectedLabels: nil,
			expectedAnnotations: map[string]string{
				"kueue.konflux-ci.dev/requests-linux-arm64": "1",
				"kueue.konflux-ci.dev/requests-linux-amd64": "1",
				"kueue.konflux-ci.dev/requests-linux-s390x": "1",
			},
			expectErr: false,
		},
		{
			name: "config.yaml old-style platforms expression - with pipeline spec without tasks",
			expressions: []string{
				oldStylePlatformsExpression,
			},
			initialLabels:       nil,
			initialAnnotations:  nil,
			initialParams:       nil,
			pipelineSpec:        &tekv1.PipelineSpec{Description: "test pipeline"},
			expectedLabels:      nil,
			expectedAnnotations: nil,
			expectErr:           false,
		},
		{
			name: "config.yaml old-style platforms expression - no pipeline spec",
			expressions: []string{
				oldStylePlatformsExpression,
			},
			initialLabels:       nil,
			initialAnnotations:  nil,
			initialParams:       nil,
			pipelineSpec:        nil,
			expectedLabels:      nil,
			expectedAnnotations: nil,
			expectErr:           false,
		},
		{
			name: "config.yaml old-style platforms expression - pipeline spec without platforms",
			expressions: []string{
				oldStylePlatformsExpression,
			},
			initialLabels:      nil,
			initialAnnotations: nil,
			initialParams:      nil,
			pipelineSpec: &tekv1.PipelineSpec{
				Tasks: getPipelineTasksWithoutPlatforms(),
			},
			expectedLabels:      nil,
			expectedAnnotations: nil,
			expectErr:           false,
		},
		{
			name: "config.yaml priority expression - pac push event",
			expressions: []string{
				complexPriorityExpression,
			},
			initialLabels: map[string]string{
				"pipelinesascode.tekton.dev/event-type": "push",
			},
			initialAnnotations: nil,
			expectedLabels: map[string]string{
				"pipelinesascode.tekton.dev/event-type": "push",
				"kueue.x-k8s.io/priority-class":         "konflux-post-merge-build",
			},
			expectedAnnotations: nil,
			expectErr:           false,
		},
		{
			name: "config.yaml priority expression - pac pull request event",
			expressions: []string{
				complexPriorityExpression,
			},
			initialLabels: map[string]string{
				"pipelinesascode.tekton.dev/event-type": "pull_request",
			},
			initialAnnotations: nil,
			expectedLabels: map[string]string{
				"pipelinesascode.tekton.dev/event-type": "pull_request",
				"kueue.x-k8s.io/priority-class":         "konflux-pre-merge-build",
			},
			expectedAnnotations: nil,
			expectErr:           false,
		},
		{
			name: "config.yaml priority expression - pac test push event",
			expressions: []string{
				complexPriorityExpression,
			},
			initialLabels: map[string]string{
				"pac.test.appstudio.openshift.io/event-type": "push",
			},
			initialAnnotations: nil,
			expectedLabels: map[string]string{
				"pac.test.appstudio.openshift.io/event-type": "push",
				"kueue.x-k8s.io/priority-class":              "konflux-post-merge-test",
			},
			expectedAnnotations: nil,
			expectErr:           false,
		},
		{
			name: "config.yaml priority expression - pac test pull request event",
			expressions: []string{
				complexPriorityExpression,
			},
			initialLabels: map[string]string{
				"pac.test.appstudio.openshift.io/event-type": "pull_request",
			},
			initialAnnotations: nil,
			expectedLabels: map[string]string{
				"pac.test.appstudio.openshift.io/event-type": "pull_request",
				"kueue.x-k8s.io/priority-class":              "konflux-pre-merge-test",
			},
			expectedAnnotations: nil,
			expectErr:           false,
		},
		{
			name: "config.yaml priority expression - managed release",
			expressions: []string{
				complexPriorityExpression,
			},
			initialLabels: map[string]string{
				"appstudio.openshift.io/service":        "release",
				"pipelines.appstudio.openshift.io/type": "managed",
			},
			initialAnnotations: nil,
			expectedLabels: map[string]string{
				"appstudio.openshift.io/service":        "release",
				"pipelines.appstudio.openshift.io/type": "managed",
				"kueue.x-k8s.io/priority-class":         "konflux-release",
			},
			expectedAnnotations: nil,
			expectErr:           false,
		},
		{
			name: "config.yaml priority expression - tenant release",
			expressions: []string{
				complexPriorityExpression,
			},
			initialLabels: map[string]string{
				"appstudio.openshift.io/service":        "release",
				"pipelines.appstudio.openshift.io/type": "tenant",
			},
			initialAnnotations: nil,
			expectedLabels: map[string]string{
				"appstudio.openshift.io/service":        "release",
				"pipelines.appstudio.openshift.io/type": "tenant",
				"kueue.x-k8s.io/priority-class":         "konflux-tenant-release",
			},
			expectedAnnotations: nil,
			expectErr:           false,
		},
		{
			name: "config.yaml priority expression - mintmaker namespace",
			expressions: []string{
				complexPriorityExpression,
			},
			namespace:          "mintmaker",
			initialLabels:      nil,
			initialAnnotations: nil,
			expectedLabels: map[string]string{
				"kueue.x-k8s.io/priority-class": "konflux-dependency-update",
			},
			expectedAnnotations: nil,
			expectErr:           false,
		},
		{
			name: "config.yaml priority expression - default fallback",
			expressions: []string{
				complexPriorityExpression,
			},
			initialLabels:      nil,
			initialAnnotations: nil,
			expectedLabels: map[string]string{
				"kueue.x-k8s.io/priority-class": "konflux-default",
			},
			expectedAnnotations: nil,
			expectErr:           false,
		},
		{
			name: "config.yaml priority expression - release service but not managed/tenant",
			expressions: []string{
				complexPriorityExpression,
			},
			initialLabels: map[string]string{
				"appstudio.openshift.io/service":        "release",
				"pipelines.appstudio.openshift.io/type": "other",
			},
			initialAnnotations: nil,
			expectedLabels: map[string]string{
				"appstudio.openshift.io/service":        "release",
				"pipelines.appstudio.openshift.io/type": "other",
				"kueue.x-k8s.io/priority-class":         "konflux-default",
			},
			expectedAnnotations: nil,
			expectErr:           false,
		},
		{
			name: "config.yaml combined expressions - all three",
			expressions: []string{
				buildPlatformsExpression,
				oldStylePlatformsExpression,
				complexPriorityExpression,
			},
			initialLabels: map[string]string{
				"pipelinesascode.tekton.dev/event-type": "push",
			},
			initialAnnotations: nil,
			initialParams:      getBuildPlatformsParamsSmall(),
			expectedLabels: map[string]string{
				"pipelinesascode.tekton.dev/event-type": "push",
				"kueue.x-k8s.io/priority-class":         "konflux-post-merge-build",
			},
			expectedAnnotations: map[string]string{
				"kueue.konflux-ci.dev/requests-linux-arm64": "1",
				"kueue.konflux-ci.dev/requests-linux-amd64": "1",
			},
			expectErr: false,
		},
		{
			name: "single resource mutation - new annotation",
			expressions: []string{
				`resource("aws-vm-x", 1000)`,
			},
			initialLabels:      nil,
			initialAnnotations: nil,
			expectedLabels:     nil,
			expectedAnnotations: map[string]string{
				"kueue.konflux-ci.dev/requests-aws-vm-x": "1000",
			},
			expectErr: false,
		},
		{
			name: "resource mutation - sum with existing annotation",
			expressions: []string{
				`resource("aws-vm-y", 2048)`,
			},
			initialLabels: nil,
			initialAnnotations: map[string]string{
				"kueue.konflux-ci.dev/requests-aws-vm-y": "1024",
			},
			expectedLabels: nil,
			expectedAnnotations: map[string]string{
				"kueue.konflux-ci.dev/requests-aws-vm-y": "3072", // 1024 + 2048
			},
			expectErr: false,
		},
		{
			name: "resource mutation - invalid existing value",
			expressions: []string{
				`resource("ibm-vm-z", 500)`,
			},
			initialLabels: nil,
			initialAnnotations: map[string]string{
				"kueue.konflux-ci.dev/requests-ibm-vm-z": "invalid",
			},
			expectedLabels:      nil,
			expectedAnnotations: nil,
			expectErr:           true,
			expectedErrType:     new(*EvaluationError),
			errMsg:              "failed to parse existing resource value \"invalid\" as integer",
		},
		{
			name: "multiple resource mutations - same key summing",
			expressions: []string{
				`resource("aws-vm-x", 2)`,
				`resource("aws-vm-x", 4)`,
			},
			initialLabels:      nil,
			initialAnnotations: nil,
			expectedLabels:     nil,
			expectedAnnotations: map[string]string{
				"kueue.konflux-ci.dev/requests-aws-vm-x": "6", // 2 + 4
			},
			expectErr: false,
		},
		{
			name: "mixed mutations with resources",
			expressions: []string{
				`[annotation("tekton.dev/pipeline", "test"), label("env", "prod"), resource("aws-vm-y", 1000)]`,
			},
			initialLabels:      nil,
			initialAnnotations: nil,
			expectedLabels: map[string]string{
				"env": "prod",
			},
			expectedAnnotations: map[string]string{
				"tekton.dev/pipeline":                    "test",
				"kueue.konflux-ci.dev/requests-aws-vm-y": "1000",
			},
			expectErr: false,
		},
		{
			name: "resource mutation - zero value",
			expressions: []string{
				`resource("ibm-vm-z", 0)`,
			},
			initialLabels:      nil,
			initialAnnotations: nil,
			expectedLabels:     nil,
			expectedAnnotations: map[string]string{
				"kueue.konflux-ci.dev/requests-ibm-vm-z": "0",
			},
			expectErr: false,
		},
		{
			name: "managedBy with custom value",
			expressions: []string{
				`managedBy("my-custom-controller.io")`,
			},
			initialLabels:       nil,
			initialAnnotations:  nil,
			expectedLabels:      nil,
			expectedAnnotations: nil,
			expectErr:           false,
		},
		{
			name: "multiKueue zero-arg sets managedBy",
			expressions: []string{
				`multiKueue()`,
			},
			initialLabels:       nil,
			initialAnnotations:  nil,
			expectedLabels:      nil,
			expectedAnnotations: nil,
			expectErr:           false,
		},
		{
			name: "multiKueue with queue name sets managedBy and queue label",
			expressions: []string{
				`multiKueue("mk-queue")`,
			},
			initialLabels:      nil,
			initialAnnotations: nil,
			expectedLabels: map[string]string{
				"kueue.x-k8s.io/queue-name": "mk-queue",
			},
			expectedAnnotations: nil,
			expectErr:           false,
		},
		{
			name: "multiKueue combined with other mutations",
			expressions: []string{
				`[multiKueue(), resource("cpu", 2)]`,
			},
			initialLabels:      nil,
			initialAnnotations: nil,
			expectedLabels:     nil,
			expectedAnnotations: map[string]string{
				"kueue.konflux-ci.dev/requests-cpu": "2",
			},
			expectErr: false,
		},
		{
			name: "conditional multiKueue - build event routes to multikueue",
			expressions: []string{
				`pacEventType == "build" ? multiKueue() : annotation("routing", "hub")`,
			},
			initialLabels: map[string]string{
				"pipelinesascode.tekton.dev/event-type": "build",
			},
			initialAnnotations: nil,
			expectedLabels: map[string]string{
				"pipelinesascode.tekton.dev/event-type": "build",
			},
			expectedAnnotations: nil,
			expectErr:           false,
		},
		{
			name: "conditional multiKueue - release event stays on hub",
			expressions: []string{
				`pacEventType == "build" ? multiKueue() : annotation("routing", "hub")`,
			},
			initialLabels: map[string]string{
				"pipelinesascode.tekton.dev/event-type": "release",
			},
			initialAnnotations: nil,
			expectedLabels: map[string]string{
				"pipelinesascode.tekton.dev/event-type": "release",
			},
			expectedAnnotations: map[string]string{
				"routing": "hub",
			},
			expectErr: false,
		},
		{
			name: "conditional multiKueue with queue override",
			expressions: []string{
				`pacEventType == "build" ? multiKueue("mk-queue") : annotation("routing", "hub")`,
			},
			initialLabels: map[string]string{
				"pipelinesascode.tekton.dev/event-type": "build",
			},
			initialAnnotations: nil,
			expectedLabels: map[string]string{
				"pipelinesascode.tekton.dev/event-type": "build",
				"kueue.x-k8s.io/queue-name":             "mk-queue",
			},
			expectedAnnotations: nil,
			expectErr:           false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)

			// Determine namespace to use
			namespace := tt.namespace
			if namespace == "" {
				namespace = "test-namespace"
			}

			// Create PipelineRun with initial state
			pipelineRun := &tekv1.PipelineRun{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "test-pipeline",
					Namespace:   namespace,
					Labels:      maps.Clone(tt.initialLabels),
					Annotations: maps.Clone(tt.initialAnnotations),
				},
				Spec: tekv1.PipelineRunSpec{
					PipelineRef: &tekv1.PipelineRef{
						Name: "test-pipeline",
					},
					Params: tt.initialParams,
				},
			}

			// Add pipeline spec if provided
			if tt.pipelineSpec != nil {
				pipelineRun.Spec.PipelineRef = nil
				pipelineRun.Spec.PipelineSpec = tt.pipelineSpec
			}

			// Compile programs and create mutator
			programs, err := CompileCELPrograms(tt.expressions)
			g.Expect(err).NotTo(HaveOccurred())

			mutator := NewCELMutator(programs)

			// Apply mutations
			err = mutator.Mutate(pipelineRun)

			// Check for expected errors
			if tt.expectErr {
				g.Expect(err).To(HaveOccurred())
				if tt.errMsg != "" {
					g.Expect(err.Error()).To(ContainSubstring(tt.errMsg))
				}
				if tt.expectedErrType != nil {
					g.Expect(errors.As(err, tt.expectedErrType)).To(BeTrue(), "expected %T, got %T: %v", tt.expectedErrType, err, err)
				}
				return
			}

			g.Expect(err).NotTo(HaveOccurred())

			// Verify labels
			g.Expect(pipelineRun.Labels).To(Equal(tt.expectedLabels))

			// Verify annotations
			g.Expect(pipelineRun.Annotations).To(Equal(tt.expectedAnnotations))
		})
	}
}

func TestCELMutator_Mutate_ManagedBy(t *testing.T) {
	tests := []struct {
		name                string
		expressions         []string
		initialLabels       map[string]string
		expectedManagedBy   *string
		expectedLabels      map[string]string
		expectedAnnotations map[string]string
	}{
		{
			name:              "managedBy sets Spec.ManagedBy",
			expressions:       []string{`managedBy("my-custom-controller.io")`},
			expectedManagedBy: ptr.To("my-custom-controller.io"),
		},
		{
			name:              "multiKueue sets Spec.ManagedBy to multikueue value",
			expressions:       []string{`multiKueue()`},
			expectedManagedBy: ptr.To("kueue.x-k8s.io/multikueue"),
		},
		{
			name:              "multiKueue with queue sets both managedBy and queue label",
			expressions:       []string{`multiKueue("mk-queue")`},
			expectedManagedBy: ptr.To("kueue.x-k8s.io/multikueue"),
			expectedLabels: map[string]string{
				"kueue.x-k8s.io/queue-name": "mk-queue",
			},
		},
		{
			name:        "conditional multiKueue - true branch sets managedBy",
			expressions: []string{`pacEventType == "build" ? multiKueue() : annotation("routing", "hub")`},
			initialLabels: map[string]string{
				"pipelinesascode.tekton.dev/event-type": "build",
			},
			expectedManagedBy: ptr.To("kueue.x-k8s.io/multikueue"),
		},
		{
			name:        "conditional multiKueue - false branch does not set managedBy",
			expressions: []string{`pacEventType == "build" ? multiKueue() : annotation("routing", "hub")`},
			initialLabels: map[string]string{
				"pipelinesascode.tekton.dev/event-type": "release",
			},
			expectedManagedBy: nil,
			expectedAnnotations: map[string]string{
				"routing": "hub",
			},
		},
		{
			name:              "multiKueue combined with other mutations",
			expressions:       []string{`[multiKueue(), resource("cpu", 2)]`},
			expectedManagedBy: ptr.To("kueue.x-k8s.io/multikueue"),
			expectedAnnotations: map[string]string{
				"kueue.konflux-ci.dev/requests-cpu": "2",
			},
		},
		{
			name:        "conditional multiKueue with queue - true branch",
			expressions: []string{`pacEventType == "build" ? multiKueue("mk-queue") : annotation("routing", "hub")`},
			initialLabels: map[string]string{
				"pipelinesascode.tekton.dev/event-type": "build",
			},
			expectedManagedBy: ptr.To("kueue.x-k8s.io/multikueue"),
			expectedLabels: map[string]string{
				"pipelinesascode.tekton.dev/event-type": "build",
				"kueue.x-k8s.io/queue-name":             "mk-queue",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)

			pipelineRun := &tekv1.PipelineRun{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pipeline",
					Namespace: "test-namespace",
					Labels:    maps.Clone(tt.initialLabels),
				},
				Spec: tekv1.PipelineRunSpec{
					PipelineRef: &tekv1.PipelineRef{Name: "test-pipeline"},
				},
			}

			programs, err := CompileCELPrograms(tt.expressions)
			g.Expect(err).NotTo(HaveOccurred())

			mutator := NewCELMutator(programs)
			g.Expect(mutator.Mutate(pipelineRun)).To(Succeed())

			if tt.expectedManagedBy != nil {
				g.Expect(pipelineRun.Spec.ManagedBy).NotTo(BeNil())
				g.Expect(*pipelineRun.Spec.ManagedBy).To(Equal(*tt.expectedManagedBy))
			} else {
				g.Expect(pipelineRun.Spec.ManagedBy).To(BeNil())
			}

			if tt.expectedLabels != nil {
				g.Expect(pipelineRun.Labels).To(Equal(tt.expectedLabels))
			}
			if tt.expectedAnnotations != nil {
				g.Expect(pipelineRun.Annotations).To(Equal(tt.expectedAnnotations))
			}
		})
	}
}

func TestCELMutator_Mutate_NilPipelineRun(t *testing.T) {
	g := NewWithT(t)

	programs, err := CompileCELPrograms([]string{
		`annotation("test-key", "test-value")`,
	})
	g.Expect(err).NotTo(HaveOccurred())

	mutator := NewCELMutator(programs)
	err = mutator.Mutate(nil)

	g.Expect(err).To(HaveOccurred())
}

func TestCELMutator_Mutate_ValidationError(t *testing.T) {
	g := NewWithT(t)

	programs, err := CompileCELPrograms([]string{
		`label("env", "test")`,
	})
	g.Expect(err).NotTo(HaveOccurred())

	mutator := NewCELMutator(programs)

	// PipelineRun with neither pipelineRef nor pipelineSpec is invalid
	pipelineRun := &tekv1.PipelineRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pipeline",
			Namespace: "test-namespace",
		},
	}

	err = mutator.Mutate(pipelineRun)
	g.Expect(err).To(HaveOccurred())

	var validationErr *ValidationError
	g.Expect(errors.As(err, &validationErr)).To(BeTrue(), "expected *ValidationError, got %T", err)
	g.Expect(err.Error()).To(ContainSubstring("expected exactly one, got neither: pipelineRef, pipelineSpec"))

	// Verify it is NOT an EvaluationError
	var evalErr *EvaluationError
	g.Expect(errors.As(err, &evalErr)).To(BeFalse(), "should not be *EvaluationError")
}

func TestCELMutator_EmptyPrograms(t *testing.T) {
	g := NewWithT(t)

	mutator := NewCELMutator([]*CompiledProgram{})

	pipelineRun := &tekv1.PipelineRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pipeline",
			Namespace: "test-namespace",
		},
	}

	err := mutator.Mutate(pipelineRun)
	g.Expect(err).NotTo(HaveOccurred())

	// Should not crash or modify the PipelineRun
	g.Expect(pipelineRun.Labels).To(BeNil())
	g.Expect(pipelineRun.Annotations).To(BeNil())
}
