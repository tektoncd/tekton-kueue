package cel

import (
	"fmt"
	"strings"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
	"k8s.io/apimachinery/pkg/util/validation"
)

// Annotation values can be up to 256KB and contain any UTF-8 characters
// The main constraint is the size limit
const maxAnnotationValueSize = 256 * 1024 // 256KB

// CompileCELPrograms compiles a list of CEL expressions into type-safe programs
func CompileCELPrograms(expressions []string) ([]*CompiledProgram, error) {
	if len(expressions) == 0 {
		return nil, fmt.Errorf("expressions list cannot be empty")
	}

	env, err := createCELEnvironment()
	if err != nil {
		return nil, fmt.Errorf("failed to create CEL environment: %w", err)
	}

	programs := make([]*CompiledProgram, 0, len(expressions))
	for i, expr := range expressions {
		if expr == "" {
			return nil, fmt.Errorf("expression %d cannot be empty", i)
		}

		program, err := compileSingleExpression(env, expr)
		if err != nil {
			return nil, fmt.Errorf("failed to compile expression %d (%q): %w", i, expr, err)
		}
		programs = append(programs, program)
	}

	return programs, nil
}

// createCELEnvironment sets up a type-safe CEL environment with PipelineRun context
func createCELEnvironment() (*cel.Env, error) {
	// Define the MutationRequest type structure for return type validation
	mutationRequestType := cel.MapType(cel.StringType, cel.AnyType)

	// Create CEL environment with proper type declarations
	env, err := cel.NewEnv(

		cel.Variable("pipelineRun", cel.MapType(cel.StringType, cel.AnyType)),
		cel.Variable("plrNamespace", cel.StringType),
		cel.Variable("pacEventType", cel.StringType),
		cel.Variable("pacTestEventType", cel.StringType),
		// Add type-safe functions for creating MutationRequests
		createMutationFunction("annotation", MutationTypeAnnotation, mutationRequestType),
		createMutationFunction("label", MutationTypeLabel, mutationRequestType),
		createResourceMutationFunction("resource", MutationTypeResource, mutationRequestType),
		createPriorityMutationFunction("priority", mutationRequestType),
		createManagedByFunction("managedBy", mutationRequestType),
		createMultiKueueFunction("multiKueue", mutationRequestType),
		// Add string manipulation functions
		createReplaceFunction("replace"),

		// Enable standard library functions
		cel.StdLib(),
	)

	if err != nil {
		return nil, fmt.Errorf("failed to create type-safe CEL environment: %w", err)
	}

	return env, nil
}

// createMutationFunction creates a CEL function for the specified mutation type
func createMutationFunction(name string, mutationType MutationType, returnType *cel.Type) cel.EnvOption {
	return cel.Function(
		name,
		cel.Overload(
			name+"_string_string_to_mutation",
			[]*cel.Type{cel.StringType, cel.StringType},
			returnType,
			cel.BinaryBinding(func(lhs, rhs ref.Val) ref.Val {
				key, keyOk := lhs.Value().(string)
				value, valueOk := rhs.Value().(string)

				if !keyOk || !valueOk {
					return types.NewErr("%s function requires string arguments", name)
				}

				if key == "" {
					return types.NewErr("%s key cannot be empty", name)
				}

				// Validate key based on mutation type
				var err error
				switch mutationType {
				case MutationTypeAnnotation:
					err = validateKey(key, "annotation")
				case MutationTypeLabel:
					err = validateKey(key, "label")
				}

				if err != nil {
					return types.NewErr("%s key validation failed: %v", name, err)
				}

				// Validate value based on mutation type
				switch mutationType {
				case MutationTypeAnnotation:
					err = validateAnnotationValue(value)
				case MutationTypeLabel:
					err = validateLabelValue(value)
				}

				if err != nil {
					return types.NewErr("%s value validation failed: %v", name, err)
				}

				// Create strongly-typed MutationRequest structure as map
				mutationMap := map[string]interface{}{
					"type":  string(mutationType),
					"key":   key,
					"value": value,
				}

				return types.NewStringInterfaceMap(types.DefaultTypeAdapter, mutationMap)
			}),
		),
	)
}

// createResourceMutationFunction creates a CEL function for resource mutations that accepts string key and int value
func createResourceMutationFunction(name string, mutationType MutationType, returnType *cel.Type) cel.EnvOption {
	return cel.Function(
		name,
		cel.Overload(
			name+"_string_int_to_mutation",
			[]*cel.Type{cel.StringType, cel.IntType},
			returnType,
			cel.BinaryBinding(func(lhs, rhs ref.Val) ref.Val {
				key, keyOk := lhs.Value().(string)

				if !keyOk {
					return types.NewErr("%s function requires string key argument", name)
				}

				if key == "" {
					return types.NewErr("%s key cannot be empty", name)
				}

				intValue, intValueOk := rhs.Value().(int64)

				if !intValueOk {
					return types.NewErr("%s function requires int value argument", name)
				}

				// Validate that the value is positive (non-negative)
				if intValue < 0 {
					return types.NewErr("%s value must be positive (>= 0), got %d", name, intValue)
				}

				// Convert int value to string for storage
				value := fmt.Sprintf("%d", intValue)

				// Validate key using annotation validation since resource mutations create annotations
				err := validateKey(key, "resource annotation")
				if err != nil {
					return types.NewErr("%s key validation failed: %v", name, err)
				}

				// Validate the converted value using annotation validation
				err = validateAnnotationValue(value)
				if err != nil {
					return types.NewErr("%s value validation failed: %v", name, err)
				}

				// Create strongly-typed MutationRequest structure as map
				// Note: This mutation type creates annotations but with special summing behavior for duplicates
				mutationMap := map[string]interface{}{
					"type":  string(mutationType),
					"key":   "kueue.konflux-ci.dev/requests-" + key,
					"value": value,
				}

				return types.NewStringInterfaceMap(types.DefaultTypeAdapter, mutationMap)
			}),
		),
	)
}

// createPriorityMutationFunction creates a CEL function for priority mutations with hardcoded key
func createPriorityMutationFunction(name string, returnType *cel.Type) cel.EnvOption {
	return cel.Function(
		name,
		cel.Overload(
			name+"_string_to_mutation",
			[]*cel.Type{cel.StringType},
			returnType,
			cel.UnaryBinding(func(val ref.Val) ref.Val {
				value, valueOk := val.Value().(string)

				if !valueOk {
					return types.NewErr("%s function requires string argument", name)
				}

				// Create strongly-typed MutationRequest structure as map with hardcoded key
				mutationMap := map[string]interface{}{
					"type":  string(MutationTypeLabel),
					"key":   "kueue.x-k8s.io/priority-class",
					"value": value,
				}

				return types.NewStringInterfaceMap(types.DefaultTypeAdapter, mutationMap)
			}),
		),
	)
}

// createManagedByFunction creates a CEL function that sets the PipelineRun's managedBy field to any value.
// Usage: managedBy("my-custom-controller.io")
func createManagedByFunction(name string, returnType *cel.Type) cel.EnvOption {
	return cel.Function(
		name,
		cel.Overload(
			name+"_string_to_mutation",
			[]*cel.Type{cel.StringType},
			returnType,
			cel.UnaryBinding(func(val ref.Val) ref.Val {
				value, valueOk := val.Value().(string)
				if !valueOk {
					return types.NewErr("%s function requires string argument", name)
				}
				if value == "" {
					return types.NewErr("%s value cannot be empty", name)
				}

				mutationMap := map[string]interface{}{
					"type":  string(MutationTypeManagedBy),
					"key":   "managedBy",
					"value": value,
				}
				return types.NewStringInterfaceMap(types.DefaultTypeAdapter, mutationMap)
			}),
		),
	)
}

// createMultiKueueFunction creates a CEL function with two overloads for MultiKueue routing.
// Both overloads return the same map<string, any> type (MutationTypeMultiKueue).
// Zero-arg: multiKueue() — sets managedBy to the MultiKueue value.
// Single-arg: multiKueue(queueName) — sets managedBy AND overrides the queue label.
// The queue label expansion is handled at apply time in mutate().
func createMultiKueueFunction(name string, returnType *cel.Type) cel.EnvOption {
	return cel.Function(
		name,
		cel.Overload(
			name+"_to_mutation",
			[]*cel.Type{},
			returnType,
			cel.FunctionBinding(func(args ...ref.Val) ref.Val {
				mutationMap := map[string]interface{}{
					"type":  string(MutationTypeMultiKueue),
					"key":   "managedBy", // required by evaluator but unused — mutator hardcodes Spec.ManagedBy
					"value": "",
				}
				return types.NewStringInterfaceMap(types.DefaultTypeAdapter, mutationMap)
			}),
		),
		cel.Overload(
			name+"_string_to_mutation",
			[]*cel.Type{cel.StringType},
			returnType,
			cel.UnaryBinding(func(val ref.Val) ref.Val {
				queueName, queueOk := val.Value().(string)
				if !queueOk {
					return types.NewErr("%s function requires string argument for queue name", name)
				}
				if queueName == "" {
					return types.NewErr("%s queue name cannot be empty", name)
				}

				if err := validateLabelValue(queueName); err != nil {
					return types.NewErr("%s queue name validation failed: %v", name, err)
				}

				mutationMap := map[string]interface{}{
					"type":  string(MutationTypeMultiKueue),
					"key":   "managedBy", // required by evaluator but unused — mutator hardcodes Spec.ManagedBy
					"value": queueName,
				}
				return types.NewStringInterfaceMap(types.DefaultTypeAdapter, mutationMap)
			}),
		),
	)
}

// createReplaceFunction creates a CEL function for string replacement
func createReplaceFunction(name string) cel.EnvOption {
	return cel.Function(
		name,
		cel.Overload(
			name+"_string_string_string_to_string",
			[]*cel.Type{cel.StringType, cel.StringType, cel.StringType},
			cel.StringType,
			cel.FunctionBinding(func(args ...ref.Val) ref.Val {
				if len(args) != 3 {
					return types.NewErr("%s function requires exactly 3 arguments", name)
				}

				source, sourceOk := args[0].Value().(string)
				search, searchOk := args[1].Value().(string)
				replacement, replacementOk := args[2].Value().(string)

				if !sourceOk || !searchOk || !replacementOk {
					return types.NewErr("%s function requires string arguments", name)
				}

				result := strings.ReplaceAll(source, search, replacement)
				return types.String(result)
			}),
		),
	)
}

// isValidOutputType checks if the CEL expression returns a valid type
// Valid return types: map<string, any> or list<map<string, any>>
func isValidOutputType(outputType *cel.Type) bool {
	switch outputType.Kind() {
	case cel.MapKind:
		return outputType.Parameters()[0].Kind() == cel.StringKind
	case cel.ListKind:
		elementType := outputType.Parameters()[0]
		return elementType.Kind() == cel.MapKind && elementType.Parameters()[0].Kind() == cel.StringKind
	default:
		return false
	}
}

// validateExpressionReturnType validates that a CEL expression returns the expected type
func validateExpressionReturnType(ast *cel.Ast) error {
	if !isValidOutputType(ast.OutputType()) {
		return fmt.Errorf("expression must return MutationRequest-compatible map<string, any> or list<map<string, any>>, got %v", ast.OutputType())
	}
	return nil
}

// compileSingleExpression compiles a single CEL expression with comprehensive type checking
func compileSingleExpression(env *cel.Env, expression string) (*CompiledProgram, error) {
	// Parse the expression with type checking
	ast, issues := env.Compile(expression)
	if issues != nil && issues.Err() != nil {
		return nil, fmt.Errorf("type checking failed for expression %q: %w", expression, issues.Err())
	}

	// Validate the output type matches our expected return types
	if err := validateExpressionReturnType(ast); err != nil {
		return nil, fmt.Errorf("invalid return type for expression %q: %w", expression, err)
	}

	// Create the program
	program, err := env.Program(ast)
	if err != nil {
		return nil, fmt.Errorf("program creation failed for expression %q: %w", expression, err)
	}

	return &CompiledProgram{
		program:    program,
		ast:        ast,
		expression: expression,
	}, nil
}

// validateKey validates that a key conforms to Kubernetes constraints
// keyType should be "label" or "annotation" for error messages
func validateKey(key, keyType string) error {
	if key == "" {
		return fmt.Errorf("%s key cannot be empty", keyType)
	}

	// Use official Kubernetes validation for keys
	// Both labels and annotations use the same qualified name validation
	if errs := validation.IsQualifiedName(key); len(errs) > 0 {
		return fmt.Errorf("%s key '%s' is invalid: %s", keyType, key, strings.Join(errs, ", "))
	}

	return nil
}

// validateLabelValue validates that a label value conforms to Kubernetes constraints
func validateLabelValue(value string) error {
	// Use official Kubernetes validation for label values
	if errs := validation.IsValidLabelValue(value); len(errs) > 0 {
		return fmt.Errorf("label value '%s' is invalid: %s", value, strings.Join(errs, ", "))
	}

	return nil
}

// validateAnnotationValue validates that an annotation value conforms to Kubernetes constraints
func validateAnnotationValue(value string) error {
	if len(value) > maxAnnotationValueSize {
		return fmt.Errorf("annotation value is too long: %d bytes, maximum allowed is %d bytes", len(value), maxAnnotationValueSize)
	}

	return nil
}
