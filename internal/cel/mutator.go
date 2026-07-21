package cel

import (
	"context"
	"fmt"
	"strconv"

	"github.com/konflux-ci/tekton-kueue/pkg/common"
	tekv1 "github.com/tektoncd/pipeline/pkg/apis/pipeline/v1"
	"k8s.io/utils/ptr"
)

// CELMutator applies mutations to PipelineRun objects based on compiled CEL programs.
// It evaluates CEL expressions and applies the resulting mutations to modify
// PipelineRun labels and annotations.
//
// Example usage:
//
//	programs, err := CompileCELPrograms([]string{
//		`annotation("tekton.dev/pipeline", "my-pipeline")`,
//		`[label("env", "production"), annotation("owner", "team-a")]`,
//	})
//	if err != nil {
//		return err
//	}
//
//	mutator := &CELMutator{programs: programs}
//	err = mutator.Mutate(pipelineRun)
type CELMutator struct {
	programs []*CompiledProgram
}

// NewCELMutator creates a new CELMutator with the provided compiled programs.
// The programs will be evaluated in order when Mutate is called.
func NewCELMutator(programs []*CompiledProgram) *CELMutator {
	return &CELMutator{programs: programs}
}

// Mutate applies all configured CEL mutations to the provided PipelineRun.
// It evaluates each compiled program and applies the resulting mutations
// to the PipelineRun's labels and annotations.
//
// A deep copy of the PipelineRun with defaults applied is used for CEL
// evaluation (required for JSON marshaling in structToCELMap), but all
// mutations are applied to the original PipelineRun to avoid permanently
// overriding cluster-level defaults (e.g., TektonConfig timeouts).
//
// The PipelineRun is modified in-place. If any evaluation fails, the method
// returns an error and the PipelineRun may be partially modified.
//
// Parameters:
//   - pipelineRun: The PipelineRun to mutate. Must not be nil.
//
// Returns:
//   - error: Any error that occurred during evaluation or mutation
func (m *CELMutator) Mutate(pipelineRun *tekv1.PipelineRun) error {
	if pipelineRun == nil {
		return fmt.Errorf("pipelineRun cannot be nil")
	}

	// Nothing to evaluate — skip deep copy, defaults, and validation.
	if len(m.programs) == 0 {
		RecordMutationSuccess()
		return nil
	}

	// Deep copy, set defaults, and validate for CEL evaluation. The copy is
	// needed because structToCELMap (json.Marshal) can fail on PipelineRuns
	// without defaults (e.g., missing ParamSpec.Type). We must not set defaults
	// on the original to avoid overriding cluster-level TektonConfig defaults
	// like timeouts.
	plrCopy := pipelineRun.DeepCopy()
	plrCopy.Spec.SetDefaults(context.TODO())

	if errs := plrCopy.Spec.Validate(context.TODO()); errs != nil {
		return &ValidationError{Err: fmt.Errorf("invalid pipelinerun: %v", errs)}
	}

	mutations, err := m.evaluate(plrCopy)
	if err != nil {
		return err
	}

	for _, mutation := range mutations {
		pipelineRun, err = mutate(pipelineRun, mutation)
		if err != nil {
			RecordMutationFailure()
			return &EvaluationError{Err: fmt.Errorf("failed to apply mutation (type: %s, key: %s): %w", mutation.Type, mutation.Key, err)}
		}
	}

	RecordMutationSuccess()
	return nil
}

// evaluate runs all compiled programs against the PipelineRun and collects
// all resulting mutations. Programs are evaluated in order, and all mutations
// are collected before any are applied.
//
// Parameters:
//   - pipelineRun: The PipelineRun to evaluate against
//
// Returns:
//   - []MutationRequest: All mutations from all programs
//   - error: Any error that occurred during evaluation
func (m *CELMutator) evaluate(pipelineRun *tekv1.PipelineRun) ([]*MutationRequest, error) {
	var allMutations []*MutationRequest
	for _, program := range m.programs {
		mutations, err := program.Evaluate(pipelineRun)
		if err != nil {
			return nil, err
		}
		allMutations = append(allMutations, mutations...)
	}
	RecordEvaluationSuccess()
	return allMutations, nil
}

// mutate applies a single mutation to the PipelineRun's metadata.
// It handles label, annotation, and resource mutations, creating the respective
// maps if they don't exist. Resource mutations have special summing behavior
// for duplicate keys.
//
// Parameters:
//   - pipelineRun: The PipelineRun to mutate
//   - mutation: The mutation to apply
//
// Returns:
//   - *tekv1.PipelineRun: The modified PipelineRun (same instance)
func mutate(pipelineRun *tekv1.PipelineRun, mutation *MutationRequest) (*tekv1.PipelineRun, error) {
	switch mutation.Type {
	case MutationTypeLabel:
		if pipelineRun.Labels == nil {
			pipelineRun.Labels = make(map[string]string)
		}
		pipelineRun.Labels[mutation.Key] = mutation.Value
	case MutationTypeAnnotation:
		if pipelineRun.Annotations == nil {
			pipelineRun.Annotations = make(map[string]string)
		}
		pipelineRun.Annotations[mutation.Key] = mutation.Value
	case MutationTypeResource:
		if pipelineRun.Annotations == nil {
			pipelineRun.Annotations = make(map[string]string)
		}

		// Parse the new value as integer
		newValue, err := strconv.Atoi(mutation.Value)
		if err != nil {
			// This should never happen because we validate the value in the CEL compiler
			return nil, fmt.Errorf("failed to parse resource value %q as integer: %w", mutation.Value, err)
		}

		// Check if the key already exists and sum the values
		if existingValue, exists := pipelineRun.Annotations[mutation.Key]; exists {
			existingInt, err := strconv.Atoi(existingValue)
			if err != nil {
				// This can happen if the user has manually set the value to a non-integer
				return nil, fmt.Errorf("failed to parse existing resource value %q as integer for key %q: %w", existingValue, mutation.Key, err)
			}
			newValue += existingInt
		}

		// Store the summed value back as string
		pipelineRun.Annotations[mutation.Key] = strconv.Itoa(newValue)
	case MutationTypeManagedBy:
		pipelineRun.Spec.ManagedBy = ptr.To(mutation.Value)
	case MutationTypeMultiKueue:
		pipelineRun.Spec.ManagedBy = ptr.To(common.ManagedByMultiKueueLabel)
		if mutation.Value != "" {
			if pipelineRun.Labels == nil {
				pipelineRun.Labels = make(map[string]string)
			}
			pipelineRun.Labels[common.QueueLabel] = mutation.Value
		}
	}
	return pipelineRun, nil
}
