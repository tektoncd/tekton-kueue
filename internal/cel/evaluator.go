package cel

import (
	"encoding/json"
	"fmt"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types/ref"
	tekv1 "github.com/tektoncd/pipeline/pkg/apis/pipeline/v1"
)

// ValidationError indicates that a PipelineRun failed validation during
// CEL evaluation (e.g., json.Marshal failed on a malformed resource).
type ValidationError struct {
	Err error
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("pipelinerun validation failed: %v", e.Err)
}

func (e *ValidationError) Unwrap() error {
	return e.Err
}

// EvaluationError indicates that a CEL expression failed to evaluate.
// This represents a server-side issue (e.g., bad CEL configuration,
// runtime error in expression, or result conversion failure) rather
// than a problem with the PipelineRun itself.
type EvaluationError struct {
	Err error
}

func (e *EvaluationError) Error() string {
	return fmt.Sprintf("CEL evaluation failed: %v", e.Err)
}

func (e *EvaluationError) Unwrap() error {
	return e.Err
}

// CompiledProgram represents a type-safe compiled CEL program
// Input: *tekv1.PipelineRun
// Output: []MutationRequest
type CompiledProgram struct {
	program    cel.Program
	ast        *cel.Ast
	expression string // Store original expression for debugging
}

// Evaluate executes the compiled CEL program with a PipelineRun input
// Input type: *tekv1.PipelineRun (type-safe)
// Output type: []MutationRequest (validated)
func (cp *CompiledProgram) Evaluate(pipelineRun *tekv1.PipelineRun) ([]*MutationRequest, error) {
	if pipelineRun == nil {
		return nil, fmt.Errorf("pipelineRun cannot be nil")
	}

	pipelineRunMap, err := structToCELMap(pipelineRun)
	if err != nil {
		return nil, &ValidationError{Err: fmt.Errorf("failed to convert PipelineRun to map: %w", err)}
	}

	// Create the evaluation context
	pacEventType := ""
	pacTestEventType := ""
	if pipelineRun.Labels != nil {
		pacEventType = pipelineRun.Labels["pipelinesascode.tekton.dev/event-type"]
		pacTestEventType = pipelineRun.Labels["pac.test.appstudio.openshift.io/event-type"]
	}
	vars := map[string]interface{}{
		"pipelineRun":      pipelineRunMap,
		"plrNamespace":     pipelineRun.Namespace,
		"pacEventType":     pacEventType,
		"pacTestEventType": pacTestEventType,
	}

	// Execute the program
	out, _, err := cp.program.Eval(vars)
	if err != nil {
		RecordEvaluationFailure()
		return nil, &EvaluationError{Err: fmt.Errorf("failed to evaluate CEL expression %q: %w", cp.expression, err)}
	}

	// Convert the result to []MutationRequest with validation
	mutations, err := convertToMutationRequests(out)
	if err != nil {
		RecordEvaluationFailure()
		return nil, &EvaluationError{Err: fmt.Errorf("failed to convert result to MutationRequests for expression %q: %w", cp.expression, err)}
	}

	// Validate all mutations
	for i, mutation := range mutations {
		if err := mutation.Validate(); err != nil {
			RecordEvaluationFailure()
			return nil, &EvaluationError{Err: fmt.Errorf("invalid mutation at index %d for expression %q: %w", i, cp.expression, err)}
		}
	}

	return mutations, nil
}

// GetExpression returns the original CEL expression for debugging
func (cp *CompiledProgram) GetExpression() string {
	return cp.expression
}

// convertToMutationRequests converts CEL evaluation result to []MutationRequest with type safety
func convertToMutationRequests(result ref.Val) ([]*MutationRequest, error) {
	// Convert the CEL result to a Go native value
	nativeResult := result.Value()

	// Handle different return types
	switch v := nativeResult.(type) {
	case []interface{}:
		// Handle Go slice (from CEL list)
		mutations, err := convertListToMutations(v)
		if err != nil {
			return nil, err
		}
		return mutations, nil

	case []ref.Val:
		// Handle CEL list type containing ref.Val items
		nativeList := make([]interface{}, len(v))
		for i, item := range v {
			nativeList[i] = item.Value()
		}
		mutations, err := convertListToMutations(nativeList)
		if err != nil {
			return nil, err
		}
		return mutations, nil

	case map[string]interface{}:
		// Single MutationRequest-compatible map
		mutation, err := convertSingleMutation(v)
		if err != nil {
			return nil, fmt.Errorf("failed to convert single mutation: %w", err)
		}
		return []*MutationRequest{mutation}, nil

	default:
		return nil, fmt.Errorf("expected MutationRequest-compatible map or list, got %T", nativeResult)
	}
}

// convertListToMutations converts a list of items to []MutationRequest
func convertListToMutations(items []interface{}) ([]*MutationRequest, error) {
	mutations := make([]*MutationRequest, 0, len(items))
	for i, item := range items {
		mutation, err := convertSingleMutation(item)
		if err != nil {
			return nil, fmt.Errorf("failed to convert list item %d: %w", i, err)
		}
		mutations = append(mutations, mutation)
	}
	return mutations, nil
}

// convertSingleMutation converts a single native Go value to MutationRequest with validation
// Enforces that maps must be MutationRequest-compatible with proper structure
func convertSingleMutation(val interface{}) (*MutationRequest, error) {
	mapVal, ok := val.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("expected MutationRequest-compatible map, got %T", val)
	}

	// Extract and validate all fields
	mutationType, err := extractMutationType(mapVal)
	if err != nil {
		return nil, err
	}

	key, err := extractStringField(mapVal, fieldKey)
	if err != nil {
		return nil, err
	}
	if key == "" {
		return nil, fmt.Errorf("'key' field cannot be empty")
	}

	value, err := extractStringField(mapVal, fieldValue)
	if err != nil {
		return nil, err
	}

	return &MutationRequest{
		Type:  mutationType,
		Key:   key,
		Value: value,
	}, nil
}

// extractMutationType extracts and validates the mutation type from a map
func extractMutationType(mapVal map[string]interface{}) (MutationType, error) {
	typeVal, exists := mapVal[fieldType]
	if !exists {
		return "", fmt.Errorf("missing required 'type' field")
	}

	typeStr, ok := typeVal.(string)
	if !ok {
		return "", fmt.Errorf("'type' field must be a string, got %T", typeVal)
	}

	mutationType := MutationType(typeStr)
	if !mutationType.IsValid() {
		return "", fmt.Errorf("invalid mutation type: %q, must be one of: %v", typeStr, ValidTypes())
	}

	return mutationType, nil
}

// extractStringField extracts a string field from a map with validation
func extractStringField(mapVal map[string]interface{}, fieldName string) (string, error) {
	fieldVal, exists := mapVal[fieldName]
	if !exists {
		return "", fmt.Errorf("missing required '%s' field", fieldName)
	}

	fieldStr, ok := fieldVal.(string)
	if !ok {
		return "", fmt.Errorf("'%s' field must be a string, got %T", fieldName, fieldVal)
	}

	return fieldStr, nil
}

func structToCELMap(v interface{}) (map[string]interface{}, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var m map[string]interface{}
	err = json.Unmarshal(b, &m)
	return m, err
}
