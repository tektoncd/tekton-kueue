package cel

import (
	"fmt"
	"strings"
	"testing"

	. "github.com/onsi/gomega"
)

const (
	// Kubernetes label value length limit
	maxLabelValueLength = 63

	// Kubernetes label key length limit (for the part after the prefix)
	maxLabelKeyLength = 63

	// Kubernetes domain prefix length limit
	maxDomainPrefixLength = 253
)

func TestCompileCELPrograms_TypeSafety(t *testing.T) {
	tests := []struct {
		name        string
		expressions []string
		expectErr   bool
		errMsg      string
	}{
		{
			name: "valid type-safe expressions",
			expressions: []string{
				`annotation("test-key", "test-value")`,
				`label("env", "production")`,
				`resource("example.com/cpu", 1000)`,
				`[annotation("key1", "value1"), label("key2", "value2")]`,
				`priority("high")`,
				`managedBy("custom-controller.io")`,
				`multiKueue()`,
				`multiKueue("mk-queue")`,
				`[multiKueue(), annotation("k", "v")]`,
			},
			expectErr: false,
		},
		{
			name:        "empty expressions list",
			expressions: []string{},
			expectErr:   true,
			errMsg:      "expressions list cannot be empty",
		},
		{
			name: "empty expression",
			expressions: []string{
				`annotation("test-key", "test-value")`,
				"", // empty expression
			},
			expectErr: true,
			errMsg:    "expression 1 cannot be empty",
		},
		{
			name: "invalid function",
			expressions: []string{
				`invalid_function("test")`,
			},
			expectErr: true,
		},
		{
			name: "type error - missing arguments",
			expressions: []string{
				`annotation("test")`, // missing second argument
			},
			expectErr: true,
		},
		{
			name: "type error - wrong argument type",
			expressions: []string{
				`annotation(123, "test")`, // first argument should be string
			},
			expectErr: true,
		},
		{
			name: "type error - resource wrong first arg",
			expressions: []string{
				`resource(123, 456)`, // first argument should be string
			},
			expectErr: true,
		},
		{
			name: "type error - resource wrong second arg",
			expressions: []string{
				`resource("valid-key", "not-an-int")`, // second argument should be int
			},
			expectErr: true,
		},
		{
			name: "type error - resource missing arguments",
			expressions: []string{
				`resource("test")`, // missing second argument
			},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)
			programs, err := CompileCELPrograms(tt.expressions)

			if tt.expectErr {
				g.Expect(err).To(HaveOccurred())
				if tt.errMsg != "" {
					g.Expect(err.Error()).To(ContainSubstring(tt.errMsg))
				}
				return
			}

			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(programs).To(HaveLen(len(tt.expressions)))

			// Test GetExpression method
			for i, program := range programs {
				g.Expect(program.GetExpression()).To(Equal(tt.expressions[i]))
			}
		})
	}
}

func TestValidateExpressionReturnType_ValidCases(t *testing.T) {
	g := NewWithT(t)

	// Create a simple CEL environment for testing
	env, err := createCELEnvironment()
	g.Expect(err).NotTo(HaveOccurred())

	tests := []struct {
		name        string
		expression  string
		description string
	}{
		{
			name:        "valid single annotation",
			expression:  `annotation("key", "value")`,
			description: "Returns map<string, any> representing MutationRequest",
		},
		{
			name:        "valid annotation list",
			expression:  `[annotation("key1", "value1"), annotation("key2", "value2")]`,
			description: "Returns list<map<string, any>> representing []MutationRequest",
		},
		{
			name:        "valid mixed list",
			expression:  `[annotation("key1", "value1"), label("key2", "value2")]`,
			description: "Returns list<map<string, any>> with mixed mutation types",
		},
		{
			name:        "valid priority function",
			expression:  `priority("high")`,
			description: "Returns map<string, any> representing priority MutationRequest",
		},
		{
			name:        "valid priority in list",
			expression:  `[priority("medium"), annotation("queue", "default")]`,
			description: "Returns list<map<string, any>> with priority and annotation",
		},
		{
			name:        "valid managedBy function",
			expression:  `managedBy("custom-value")`,
			description: "Returns map<string, any> representing managedBy MutationRequest",
		},
		{
			name:        "valid multiKueue function",
			expression:  `multiKueue()`,
			description: "Returns map<string, any> representing multiKueue MutationRequest",
		},
		{
			name:        "valid multiKueue with queue",
			expression:  `multiKueue("mk-queue")`,
			description: "Returns map<string, any> representing multiKueue MutationRequest with queue",
		},
		{
			name:        "valid single resource",
			expression:  `resource("example.com/cpu", 500)`,
			description: "Returns map<string, any> representing resource MutationRequest",
		},
		{
			name:        "valid resource list",
			expression:  `[resource("aws-vm-x", 1000), resource("aws-vm-y", 2048)]`,
			description: "Returns list<map<string, any>> representing []MutationRequest with resources",
		},
		{
			name:        "valid mixed list with resource",
			expression:  `[annotation("key1", "value1"), label("key2", "value2"), resource("aws-vm-x", 500)]`,
			description: "Returns list<map<string, any>> with mixed mutation types including resource",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)

			// Compile the expression
			ast, issues := env.Compile(tt.expression)
			g.Expect(issues.Err()).NotTo(HaveOccurred(), "Expression should compile successfully")

			// Validate the return type
			err := validateExpressionReturnType(ast)
			g.Expect(err).NotTo(HaveOccurred(), tt.description)
		})
	}
}

func TestValidateExpressionReturnType_InvalidCases(t *testing.T) {
	g := NewWithT(t)

	// Create a simple CEL environment for testing
	env, err := createCELEnvironment()
	g.Expect(err).NotTo(HaveOccurred())

	tests := []struct {
		name        string
		expression  string
		description string
	}{
		{
			name:        "invalid string return",
			expression:  `"just a string"`,
			description: "Returns string instead of MutationRequest structure",
		},
		{
			name:        "invalid number return",
			expression:  `42`,
			description: "Returns number instead of MutationRequest structure",
		},
		{
			name:        "invalid list of strings",
			expression:  `["string1", "string2"]`,
			description: "Returns list<string> instead of list<map<string, any>>",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)

			// Compile the expression
			ast, issues := env.Compile(tt.expression)
			g.Expect(issues.Err()).NotTo(HaveOccurred(), "Expression should compile successfully")

			// Validate the return type
			err := validateExpressionReturnType(ast)
			g.Expect(err).To(HaveOccurred(), tt.description)
		})
	}
}

func TestCompiledProgram_GetExpression(t *testing.T) {
	g := NewWithT(t)

	expression := `annotation("test-key", "test-value")`
	programs, err := CompileCELPrograms([]string{expression})
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(programs).To(HaveLen(1))
	g.Expect(programs[0].GetExpression()).To(Equal(expression))
}

func TestReplaceFunction(t *testing.T) {
	g := NewWithT(t)

	// Create a CEL environment for testing
	env, err := createCELEnvironment()
	g.Expect(err).NotTo(HaveOccurred())

	tests := []struct {
		name       string
		expression string
		expected   string
	}{
		{
			name:       "replace forward slash with dash",
			expression: `replace("linux/amd64", "/", "-")`,
			expected:   "linux-amd64",
		},
		{
			name:       "replace multiple occurrences",
			expression: `replace("hello world hello", "hello", "hi")`,
			expected:   "hi world hi",
		},
		{
			name:       "replace with empty string",
			expression: `replace("test-value", "-", "")`,
			expected:   "testvalue",
		},
		{
			name:       "replace non-existent character",
			expression: `replace("test", "x", "y")`,
			expected:   "test",
		},
		{
			name:       "replace entire string",
			expression: `replace("old", "old", "new")`,
			expected:   "new",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)

			// Compile the expression
			ast, issues := env.Compile(tt.expression)
			g.Expect(issues.Err()).NotTo(HaveOccurred(), "Expression should compile successfully")

			// Create program and evaluate
			program, err := env.Program(ast)
			g.Expect(err).NotTo(HaveOccurred(), "Program creation should succeed")

			// Evaluate the expression
			result, _, err := program.Eval(map[string]interface{}{})
			g.Expect(err).NotTo(HaveOccurred(), "Evaluation should succeed")

			// Check the result
			g.Expect(result.Value()).To(Equal(tt.expected))
		})
	}
}

func TestKubernetesKeyValidation(t *testing.T) {
	g := NewWithT(t)

	// Create a CEL environment for testing
	env, err := createCELEnvironment()
	g.Expect(err).NotTo(HaveOccurred())

	tests := []struct {
		name        string
		expression  string
		expectError bool
		errorMsg    string
	}{
		// Valid annotation keys
		{
			name:        "valid annotation key without prefix",
			expression:  `annotation("simple-key", "value")`,
			expectError: false,
		},
		{
			name:        "valid annotation key with prefix",
			expression:  `annotation("example.com/my-key", "value")`,
			expectError: false,
		},
		{
			name:        "valid annotation key with complex prefix",
			expression:  `annotation("sub.domain.example.com/my-key", "value")`,
			expectError: false,
		},
		// Valid label keys
		{
			name:        "valid label key without prefix",
			expression:  `label("app", "value")`,
			expectError: false,
		},
		{
			name:        "valid label key with prefix",
			expression:  `label("kubernetes.io/os", "value")`,
			expectError: false,
		},
		// Invalid annotation keys
		{
			name:        "invalid annotation key - starts with dash",
			expression:  `annotation("-invalid", "value")`,
			expectError: true,
			errorMsg:    "annotation key validation failed",
		},
		{
			name:        "invalid annotation key - ends with dash",
			expression:  `annotation("invalid-", "value")`,
			expectError: true,
			errorMsg:    "annotation key validation failed",
		},
		{
			name:        "invalid annotation key - too long name",
			expression:  `annotation("` + strings.Repeat("a", maxLabelKeyLength+1) + `", "value")`,
			expectError: true,
			errorMsg:    "annotation key validation failed",
		},
		{
			name:        "invalid annotation key - multiple slashes",
			expression:  `annotation("domain.com/path/invalid", "value")`,
			expectError: true,
			errorMsg:    "annotation key validation failed",
		},
		{
			name:        "invalid annotation key - invalid prefix",
			expression:  `annotation("invalid-.com/key", "value")`,
			expectError: true,
			errorMsg:    "annotation key validation failed",
		},
		{
			name:        "invalid annotation key - prefix too long",
			expression:  `annotation("` + strings.Repeat("a", maxDomainPrefixLength+1) + `.com/key", "value")`,
			expectError: true,
			errorMsg:    "annotation key validation failed",
		},
		// Invalid label keys
		{
			name:        "invalid label key - starts with dash",
			expression:  `label("-invalid", "value")`,
			expectError: true,
			errorMsg:    "label key validation failed",
		},
		{
			name:        "invalid label key - ends with dash",
			expression:  `label("invalid-", "value")`,
			expectError: true,
			errorMsg:    "label key validation failed",
		},
		{
			name:        "invalid label key - too long name",
			expression:  `label("` + strings.Repeat("a", maxLabelKeyLength+1) + `", "value")`,
			expectError: true,
			errorMsg:    "label key validation failed",
		},
		{
			name:        "invalid label key - multiple slashes",
			expression:  `label("domain.com/path/invalid", "value")`,
			expectError: true,
			errorMsg:    "label key validation failed",
		},
		{
			name:        "invalid label key - invalid prefix",
			expression:  `label("invalid-.com/key", "value")`,
			expectError: true,
			errorMsg:    "label key validation failed",
		},
		{
			name:        "invalid label key - prefix too long",
			expression:  `label("` + strings.Repeat("a", maxDomainPrefixLength+1) + `.com/key", "value")`,
			expectError: true,
			errorMsg:    "label key validation failed",
		},
		// Invalid label values
		{
			name:        "invalid label value - starts with dash",
			expression:  `label("valid-key", "-invalid")`,
			expectError: true,
			errorMsg:    "label value validation failed",
		},
		{
			name:        "invalid label value - ends with dash",
			expression:  `label("valid-key", "invalid-")`,
			expectError: true,
			errorMsg:    "label value validation failed",
		},
		{
			name:        "invalid label value - too long",
			expression:  `label("valid-key", "` + strings.Repeat("a", maxLabelValueLength+1) + `")`,
			expectError: true,
			errorMsg:    "label value validation failed",
		},
		{
			name:        "invalid label value - contains invalid characters",
			expression:  `label("valid-key", "invalid/value")`,
			expectError: true,
			errorMsg:    "label value validation failed",
		},
		{
			name:        "invalid label value - contains spaces",
			expression:  `label("valid-key", "invalid value")`,
			expectError: true,
			errorMsg:    "label value validation failed",
		},
		{
			name:        "valid label value - empty",
			expression:  `label("valid-key", "")`,
			expectError: false,
		},
		{
			name:        "valid label value - alphanumeric",
			expression:  `label("valid-key", "valid123")`,
			expectError: false,
		},
		{
			name:        "valid label value - with dashes underscores dots",
			expression:  `label("valid-key", "valid-value_with.dots")`,
			expectError: false,
		},
		// Invalid annotation values - removed due to CEL expression size limits
		{
			name:        "valid annotation value - empty",
			expression:  `annotation("valid-key", "")`,
			expectError: false,
		},
		{
			name:        "valid annotation value - with special characters",
			expression:  `annotation("valid-key", "value with spaces and special chars: !@#$%^&*()")`,
			expectError: false,
		},
		{
			name:        "valid annotation value - unicode",
			expression:  `annotation("valid-key", "测试中文字符 🚀")`,
			expectError: false,
		},
		{
			name:        "valid annotation value - multiline",
			expression:  `annotation("valid-key", "line1\nline2\nline3")`,
			expectError: false,
		},
		// Note: Large annotation value testing is done in TestValidationFunctions
		// to avoid CEL expression size limits
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)

			// Compile the expression
			ast, issues := env.Compile(tt.expression)
			g.Expect(issues.Err()).NotTo(HaveOccurred(), "Expression should compile successfully")

			// Create program and evaluate
			program, err := env.Program(ast)
			g.Expect(err).NotTo(HaveOccurred(), "Program creation should succeed")

			// Evaluate the expression
			result, _, err := program.Eval(map[string]interface{}{})

			if tt.expectError {
				g.Expect(err).To(HaveOccurred(), "Expected evaluation to fail")
				g.Expect(err.Error()).To(ContainSubstring(tt.errorMsg))
			} else {
				g.Expect(err).NotTo(HaveOccurred(), "Expected evaluation to succeed")
				g.Expect(result).NotTo(BeNil(), "Expected valid result")
			}
		})
	}
}

func TestValidationFunctions(t *testing.T) {
	g := NewWithT(t)

	// Test validateLabelValue
	t.Run("validateLabelValue", func(t *testing.T) {
		// Valid label values
		g.Expect(validateLabelValue("")).To(Succeed())
		g.Expect(validateLabelValue("valid123")).To(Succeed())
		g.Expect(validateLabelValue("valid-value_with.dots")).To(Succeed())

		// Invalid label values
		err := validateLabelValue("-invalid")
		g.Expect(err).To(HaveOccurred())
		g.Expect(err.Error()).To(ContainSubstring("label value"))

		err = validateLabelValue("invalid-")
		g.Expect(err).To(HaveOccurred())
		g.Expect(err.Error()).To(ContainSubstring("label value"))

		err = validateLabelValue(strings.Repeat("a", maxLabelValueLength+1))
		g.Expect(err).To(HaveOccurred())
		g.Expect(err.Error()).To(ContainSubstring("label value"))

		err = validateLabelValue("invalid/value")
		g.Expect(err).To(HaveOccurred())
		g.Expect(err.Error()).To(ContainSubstring("label value"))
	})

	// Test validateAnnotationValue
	t.Run("validateAnnotationValue", func(t *testing.T) {
		// Valid annotation values
		g.Expect(validateAnnotationValue("")).To(Succeed())
		g.Expect(validateAnnotationValue("valid value with spaces")).To(Succeed())
		g.Expect(validateAnnotationValue("测试中文字符 🚀")).To(Succeed())
		g.Expect(validateAnnotationValue("line1\nline2\nline3")).To(Succeed())
		g.Expect(validateAnnotationValue(strings.Repeat("a", maxAnnotationValueSize))).To(Succeed())

		// Invalid annotation value - too long
		err := validateAnnotationValue(strings.Repeat("a", maxAnnotationValueSize+1))
		g.Expect(err).To(HaveOccurred())
		g.Expect(err.Error()).To(And(
			ContainSubstring("annotation value is too long"),
			ContainSubstring("262145 bytes"),
			ContainSubstring("maximum allowed is 262144 bytes"),
		))
	})
}

func TestResourceFunction_ValidCases(t *testing.T) {
	g := NewWithT(t)

	// Create a CEL environment for testing
	env, err := createCELEnvironment()
	g.Expect(err).NotTo(HaveOccurred())

	tests := []struct {
		name       string
		expression string
		expected   map[string]interface{}
	}{
		{
			name:       "valid resource with positive int",
			expression: `resource("aws-vm-x", 1000)`,
			expected: map[string]interface{}{
				"type":  "resource",
				"key":   "kueue.konflux-ci.dev/requests-aws-vm-x",
				"value": "1000",
			},
		},
		{
			name:       "valid resource with zero value",
			expression: `resource("aws-vm-y", 0)`,
			expected: map[string]interface{}{
				"type":  "resource",
				"key":   "kueue.konflux-ci.dev/requests-aws-vm-y",
				"value": "0",
			},
		},
		{
			name:       "valid resource with simple key",
			expression: `resource("ibm-vm-z", 2000)`,
			expected: map[string]interface{}{
				"type":  "resource",
				"key":   "kueue.konflux-ci.dev/requests-ibm-vm-z",
				"value": "2000",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)

			// Compile the expression
			ast, issues := env.Compile(tt.expression)
			g.Expect(issues.Err()).NotTo(HaveOccurred(), "Expression should compile successfully")

			// Create program and evaluate
			program, err := env.Program(ast)
			g.Expect(err).NotTo(HaveOccurred(), "Program creation should succeed")

			// Evaluate the expression
			result, _, err := program.Eval(map[string]interface{}{})

			g.Expect(err).NotTo(HaveOccurred(), "Expected evaluation to succeed")
			g.Expect(result).NotTo(BeNil(), "Expected valid result")

			// Verify the result structure
			resultMap, ok := result.Value().(map[string]interface{})
			g.Expect(ok).To(BeTrue(), "Result should be a map")
			g.Expect(resultMap).To(Equal(tt.expected), "Result should match expected structure")
		})
	}
}

func TestResourceFunction_ErrorCases(t *testing.T) {
	g := NewWithT(t)

	// Create a CEL environment for testing
	env, err := createCELEnvironment()
	g.Expect(err).NotTo(HaveOccurred())

	tests := []struct {
		name       string
		expression string
		errorMsg   string
	}{
		{
			name:       "invalid resource with negative value",
			expression: `resource("aws-vm-x", -500)`,
			errorMsg:   "resource value must be positive (>= 0), got -500",
		},
		{
			name:       "invalid resource with empty key",
			expression: `resource("", 100)`,
			errorMsg:   "resource key cannot be empty",
		},
		{
			name:       "invalid resource key - starts with dash",
			expression: `resource("-invalid", 100)`,
			errorMsg:   "resource key validation failed",
		},
		{
			name:       "invalid resource key - ends with dash",
			expression: `resource("invalid-", 100)`,
			errorMsg:   "resource key validation failed",
		},
		{
			name:       "invalid resource key - multiple slashes",
			expression: `resource("domain.com/path/invalid", 100)`,
			errorMsg:   "resource key validation failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)

			// Compile the expression
			ast, issues := env.Compile(tt.expression)
			g.Expect(issues.Err()).NotTo(HaveOccurred(), "Expression should compile successfully")

			// Create program and evaluate
			program, err := env.Program(ast)
			g.Expect(err).NotTo(HaveOccurred(), "Program creation should succeed")

			// Evaluate the expression
			_, _, err = program.Eval(map[string]interface{}{})

			g.Expect(err).To(HaveOccurred(), "Expected evaluation to fail")
			g.Expect(err.Error()).To(ContainSubstring(tt.errorMsg), "Error message should contain expected text")
		})
	}
}

func TestManagedByFunction(t *testing.T) {
	g := NewWithT(t)

	env, err := createCELEnvironment()
	g.Expect(err).NotTo(HaveOccurred())

	tests := []struct {
		name       string
		expression string
		expected   map[string]interface{}
	}{
		{
			name:       "managedBy with custom value",
			expression: `managedBy("my-custom-controller.io")`,
			expected: map[string]interface{}{
				"type":  "managedBy",
				"key":   "managedBy",
				"value": "my-custom-controller.io",
			},
		},
		{
			name:       "managedBy with multikueue value",
			expression: `managedBy("kueue.x-k8s.io/multikueue")`,
			expected: map[string]interface{}{
				"type":  "managedBy",
				"key":   "managedBy",
				"value": "kueue.x-k8s.io/multikueue",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)

			ast, issues := env.Compile(tt.expression)
			g.Expect(issues.Err()).NotTo(HaveOccurred())

			program, err := env.Program(ast)
			g.Expect(err).NotTo(HaveOccurred())

			result, _, err := program.Eval(map[string]interface{}{})
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(result).NotTo(BeNil())

			resultMap, ok := result.Value().(map[string]interface{})
			g.Expect(ok).To(BeTrue(), "Result should be a map")
			g.Expect(resultMap).To(Equal(tt.expected))
		})
	}
}

func TestManagedByFunction_ErrorCases(t *testing.T) {
	g := NewWithT(t)

	env, err := createCELEnvironment()
	g.Expect(err).NotTo(HaveOccurred())

	tests := []struct {
		name       string
		expression string
		errorMsg   string
	}{
		{
			name:       "managedBy with empty value",
			expression: `managedBy("")`,
			errorMsg:   "managedBy value cannot be empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)

			ast, issues := env.Compile(tt.expression)
			g.Expect(issues.Err()).NotTo(HaveOccurred())

			program, err := env.Program(ast)
			g.Expect(err).NotTo(HaveOccurred())

			_, _, err = program.Eval(map[string]interface{}{})
			g.Expect(err).To(HaveOccurred())
			g.Expect(err.Error()).To(ContainSubstring(tt.errorMsg))
		})
	}
}

func TestMultiKueueFunction(t *testing.T) {
	g := NewWithT(t)

	env, err := createCELEnvironment()
	g.Expect(err).NotTo(HaveOccurred())

	t.Run("zero-arg returns multiKueue mutation without queue", func(t *testing.T) {
		g := NewWithT(t)

		ast, issues := env.Compile(`multiKueue()`)
		g.Expect(issues.Err()).NotTo(HaveOccurred())

		program, err := env.Program(ast)
		g.Expect(err).NotTo(HaveOccurred())

		result, _, err := program.Eval(map[string]interface{}{})
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(result).NotTo(BeNil())

		resultMap, ok := result.Value().(map[string]interface{})
		g.Expect(ok).To(BeTrue(), "Result should be a map")
		g.Expect(resultMap).To(Equal(map[string]interface{}{
			"type":  "multiKueue",
			"key":   "managedBy",
			"value": "",
		}))
	})

	t.Run("single-arg returns multiKueue mutation with queue", func(t *testing.T) {
		g := NewWithT(t)

		ast, issues := env.Compile(`multiKueue("mk-queue")`)
		g.Expect(issues.Err()).NotTo(HaveOccurred())

		program, err := env.Program(ast)
		g.Expect(err).NotTo(HaveOccurred())

		result, _, err := program.Eval(map[string]interface{}{})
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(result).NotTo(BeNil())

		resultMap, ok := result.Value().(map[string]interface{})
		g.Expect(ok).To(BeTrue(), "Result should be a map")
		g.Expect(resultMap).To(Equal(map[string]interface{}{
			"type":  "multiKueue",
			"key":   "managedBy",
			"value": "mk-queue",
		}))
	})

	t.Run("both overloads are composable in ternary", func(t *testing.T) {
		g := NewWithT(t)

		_, err := CompileCELPrograms([]string{
			`true ? multiKueue() : multiKueue("queue")`,
		})
		g.Expect(err).NotTo(HaveOccurred())
	})

	t.Run("both overloads composable in list", func(t *testing.T) {
		g := NewWithT(t)

		_, err := CompileCELPrograms([]string{
			`[multiKueue("mk-queue"), annotation("k", "v")]`,
		})
		g.Expect(err).NotTo(HaveOccurred())
	})
}

func TestMultiKueueFunction_ErrorCases(t *testing.T) {
	g := NewWithT(t)

	env, err := createCELEnvironment()
	g.Expect(err).NotTo(HaveOccurred())

	t.Run("single-arg with empty queue name", func(t *testing.T) {
		g := NewWithT(t)

		ast, issues := env.Compile(`multiKueue("")`)
		g.Expect(issues.Err()).NotTo(HaveOccurred())

		program, err := env.Program(ast)
		g.Expect(err).NotTo(HaveOccurred())

		_, _, err = program.Eval(map[string]interface{}{})
		g.Expect(err).To(HaveOccurred())
		g.Expect(err.Error()).To(ContainSubstring("multiKueue queue name cannot be empty"))
	})

	t.Run("single-arg with invalid label value", func(t *testing.T) {
		g := NewWithT(t)

		ast, issues := env.Compile(`multiKueue("invalid value!")`)
		g.Expect(issues.Err()).NotTo(HaveOccurred())

		program, err := env.Program(ast)
		g.Expect(err).NotTo(HaveOccurred())

		_, _, err = program.Eval(map[string]interface{}{})
		g.Expect(err).To(HaveOccurred())
		g.Expect(err.Error()).To(ContainSubstring("queue name validation failed"))
	})
}

func TestResourceFunctionIntegration(t *testing.T) {
	g := NewWithT(t)

	// Create CEL environment for testing
	env, err := createCELEnvironment()
	g.Expect(err).NotTo(HaveOccurred())

	// Test resource function in list expressions
	expressions := []string{
		`[resource("aws-vm-x", 1000), resource("aws-vm-y", 2048)]`,
		`[annotation("queue", "default"), resource("ibm-vm-z", 500)]`,
		`resource("aws-vm-x", 4)`,
	}

	for i, expression := range expressions {
		t.Run(fmt.Sprintf("expression_%d", i), func(t *testing.T) {
			g := NewWithT(t)

			// Compile the expression
			ast, issues := env.Compile(expression)
			g.Expect(issues.Err()).NotTo(HaveOccurred(), "Expression should compile successfully")

			// Create program and evaluate
			program, err := env.Program(ast)
			g.Expect(err).NotTo(HaveOccurred(), "Program creation should succeed")

			// Evaluate the expression
			result, _, err := program.Eval(map[string]interface{}{})
			g.Expect(err).NotTo(HaveOccurred(), "Program should evaluate successfully")
			g.Expect(result).NotTo(BeNil(), "Program should return a valid result")
		})
	}

	// Also test the compilation through the main CompileCELPrograms function
	programs, err := CompileCELPrograms(expressions)
	g.Expect(err).NotTo(HaveOccurred(), "All expressions should compile successfully")
	g.Expect(programs).To(HaveLen(3), "Should have compiled 3 programs")
}
