// Package cel provides CEL (Common Expression Language) compilation and evaluation
// functionality for creating MutationRequests based on PipelineRun objects.
//
// # Overview
//
// This package enables type-safe compilation and evaluation of CEL expressions
// that generate Kubernetes mutations (annotations and labels) based on Tekton
// PipelineRun data. It provides compile-time type checking and runtime validation
// to ensure mutations are well-formed and safe.
//
// # Type Safety
//
//   - Input: *tekton.PipelineRun (strongly typed and validated)
//   - Output: []MutationRequest (validated structure and content)
//   - Functions: annotation(key, value), label(key, value), priority(value), resource(key, count),
//     managedBy(value), multiKueue() / multiKueue(queueName), replace(source, search, replacement)
//   - Expressions: Single mutations or lists of mutations
//
// # Basic Usage
//
//	expressions := []string{
//		`annotation("build-info", "compiled-" + pipelineRun.metadata.name)`,
//		`label("environment", "production")`,
//		`[annotation("key1", "value1"), label("key2", "value2")]`,
//		`priority("high")`,
//	}
//
//	programs, err := cel.CompileCELPrograms(expressions)
//	if err != nil {
//		log.Fatal(err)
//	}
//
//	pipelineRun := &tekton.PipelineRun{...}
//	for _, program := range programs {
//		mutations, err := program.Evaluate(pipelineRun)
//		if err != nil {
//			log.Printf("Error: %v", err)
//			continue
//		}
//		// Apply mutations to Kubernetes resources...
//	}
//
// # CELMutator Usage
//
// For convenient mutation application, use the CELMutator:
//
//	expressions := []string{
//		`annotation("tekton.dev/pipeline", "my-pipeline")`,
//		`[label("env", "production"), annotation("owner", "team-a")]`,
//		`priority("default")`,
//	}
//
//	programs, err := cel.CompileCELPrograms(expressions)
//	if err != nil {
//		log.Fatal(err)
//	}
//
//	mutator := cel.NewCELMutator(programs)
//	pipelineRun := &tekton.PipelineRun{...}
//
//	err = mutator.Mutate(pipelineRun)
//	if err != nil {
//		log.Printf("Mutation failed: %v", err)
//	}
//	// PipelineRun is now modified with labels and annotations
//
// # Available CEL Functions
//
//   - annotation(key: string, value: string) -> MutationRequest
//     Creates an annotation mutation with the specified key and value
//
//   - label(key: string, value: string) -> MutationRequest
//     Creates a label mutation with the specified key and value
//
//   - priority(value: string) -> MutationRequest
//     Creates a label mutation with key "kueue.x-k8s.io/priority-class" and the specified value
//
//   - managedBy(value: string) -> MutationRequest
//     Sets the PipelineRun's Spec.ManagedBy field to the specified value.
//     Use for custom controller routing (e.g., managedBy("tekton.dev/pipeline"))
//
//   - multiKueue() -> MutationRequest
//     Sets Spec.ManagedBy to "kueue.x-k8s.io/multikueue", routing the PipelineRun
//     to a spoke cluster via Kueue's MultiKueue dispatch
//
//   - multiKueue(queueName: string) -> MutationRequest
//     Sets Spec.ManagedBy to "kueue.x-k8s.io/multikueue" AND overrides the
//     "kueue.x-k8s.io/queue-name" label to the specified queue. Both changes
//     are applied from a single mutation at apply time. Use when the MultiKueue
//     queue differs from the default queue.
//
//   - replace(source: string, search: string, replacement: string) -> string
//     Replaces all occurrences of search string with replacement string in the source string
//
// # Available CEL Variables
//
//   - pipelineRun: map<string, any> - The full PipelineRun object as a CEL-accessible map
//   - plrNamespace: string - The namespace of the PipelineRun
//   - pacEventType: string - Value from label "pipelinesascode.tekton.dev/event-type" (empty if not present)
//   - pacTestEventType: string - Value from label "pac.test.appstudio.openshift.io/event-type" (empty if not present)
//
// # Advanced Usage Examples
//
// Conditional mutations based on namespace:
//
//	expression := `plrNamespace == "production" ? priority("high") : priority("default")`
//
// Conditional mutations based on event type:
//
//	expression := `pacEventType == "push" ? priority("push") :
//	              pacEventType == "pull_request" ? priority("pull-request") :
//	              priority("default")`
//
// Accessing PipelineRun parameters:
//
//	expression := `has(pipelineRun.spec.params) &&
//	              pipelineRun.spec.params.exists(p, p.name == "build-platforms") ?
//	              pipelineRun.spec.params.filter(p, p.name == "build-platforms")[0].value.map(
//	                  p, annotation("kueue.konflux-ci.dev/requests-" + p, "1")
//	              ) : []`
//
// CEL-based routing with multiKueue — route builds to spoke, releases to hub:
//
//	expression := `has(pipelineRun.metadata.labels) &&
//	              "pipeline-type" in pipelineRun.metadata.labels &&
//	              pipelineRun.metadata.labels["pipeline-type"] == "build" ?
//	              multiKueue("multikueue-queue") : []`
//
// Set a custom managedBy for release pipelines:
//
//	expression := `has(pipelineRun.metadata.labels) &&
//	              "pipeline-type" in pipelineRun.metadata.labels &&
//	              pipelineRun.metadata.labels["pipeline-type"] == "release" ?
//	              [managedBy("tekton.dev/pipeline")] : []`
//
// Using string manipulation with replace function:
//
//	expression := `has(pipelineRun.spec.params) &&
//	              pipelineRun.spec.params.exists(p, p.name == "build-platforms") ?
//	              pipelineRun.spec.params.filter(p, p.name == "build-platforms")[0].value.map(
//	                  p, annotation("kueue.konflux-ci.dev/requests-" + replace(p, "/", "-"), "1")
//	              ) : []`
//
// # Package Structure
//
// This package is organized into focused modules:
//
//   - types.go: Core data types (MutationType, MutationRequest) and validation
//   - compiler.go: CEL environment setup, compilation, and type checking
//   - evaluator.go: Runtime program evaluation and result conversion
//   - mutator.go: CELMutator for convenient mutation application
//   - metrics.go: Prometheus metrics for monitoring CEL evaluation failures
//
// # Validation Hierarchy
//
//  1. Compile-time: CEL type checker validates function signatures and return types
//  2. Runtime input: Validates PipelineRun is not nil and properly structured
//  3. Runtime output: Validates returned data has correct MutationRequest structure
//  4. Field validation: Validates all required fields (type, key, value) are present and valid
//  5. Kubernetes validation: Uses official k8s.io/apimachinery validation for annotation and label keys
//
// # Error Handling
//
// The package provides detailed error messages for:
//   - Compilation failures with expression context
//   - Type mismatches with expected vs actual types
//   - Runtime evaluation errors with expression details
//   - Validation failures with field-specific information
//
// # Metrics
//
// The package exposes Prometheus metrics:
//   - tekton_kueue_cel_evaluation_failures_total: Counter for CEL evaluation failures
package cel
