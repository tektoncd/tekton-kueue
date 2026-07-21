package cel

import (
	"encoding/json"
	"testing"

	. "github.com/onsi/gomega"
)

func TestMutationType_IsValid(t *testing.T) {
	tests := []struct {
		name string
		mt   MutationType
		want bool
	}{
		{"valid annotation", MutationTypeAnnotation, true},
		{"valid label", MutationTypeLabel, true},
		{"valid resource", MutationTypeResource, true},
		{"valid managedBy", MutationTypeManagedBy, true},
		{"valid multiKueue", MutationTypeMultiKueue, true},
		{"invalid type", MutationType("invalid"), false},
		{"empty type", MutationType(""), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)
			got := tt.mt.IsValid()
			g.Expect(got).To(Equal(tt.want))
		})
	}
}

func TestMutationType_JSON(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		expectErr bool
		expected  MutationType
	}{
		{
			name:      "valid annotation",
			input:     `"annotation"`,
			expectErr: false,
			expected:  MutationTypeAnnotation,
		},
		{
			name:      "valid label",
			input:     `"label"`,
			expectErr: false,
			expected:  MutationTypeLabel,
		},
		{
			name:      "valid resource",
			input:     `"resource"`,
			expectErr: false,
			expected:  MutationTypeResource,
		},
		{
			name:      "valid managedBy",
			input:     `"managedBy"`,
			expectErr: false,
			expected:  MutationTypeManagedBy,
		},
		{
			name:      "valid multiKueue",
			input:     `"multiKueue"`,
			expectErr: false,
			expected:  MutationTypeMultiKueue,
		},
		{
			name:      "invalid type",
			input:     `"invalid"`,
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)
			var mt MutationType
			err := json.Unmarshal([]byte(tt.input), &mt)

			if tt.expectErr {
				g.Expect(err).To(HaveOccurred())
				return
			}

			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(mt).To(Equal(tt.expected))
		})
	}
}

func TestMutationRequest_Validate(t *testing.T) {
	tests := []struct {
		name      string
		request   MutationRequest
		expectErr bool
		errMsg    string
	}{
		{
			name: "valid annotation",
			request: MutationRequest{
				Type:  MutationTypeAnnotation,
				Key:   "test-key",
				Value: "test-value",
			},
			expectErr: false,
		},
		{
			name: "valid label",
			request: MutationRequest{
				Type:  MutationTypeLabel,
				Key:   "env",
				Value: "production",
			},
			expectErr: false,
		},
		{
			name: "valid resource",
			request: MutationRequest{
				Type:  MutationTypeResource,
				Key:   "example.com/resource-key",
				Value: "42",
			},
			expectErr: false,
		},
		{
			name: "invalid type",
			request: MutationRequest{
				Type:  MutationType("invalid"),
				Key:   "test-key",
				Value: "test-value",
			},
			expectErr: true,
			errMsg:    "invalid mutation type: invalid",
		},
		{
			name: "empty key",
			request: MutationRequest{
				Type:  MutationTypeAnnotation,
				Key:   "",
				Value: "test-value",
			},
			expectErr: true,
			errMsg:    "mutation key cannot be empty",
		},
		{
			name: "empty value",
			request: MutationRequest{
				Type:  MutationTypeAnnotation,
				Key:   "test-key",
				Value: "",
			},
			expectErr: true,
			errMsg:    "mutation value cannot be empty",
		},
		{
			name: "valid managedBy",
			request: MutationRequest{
				Type:  MutationTypeManagedBy,
				Key:   "managedBy",
				Value: "custom-controller.io",
			},
			expectErr: false,
		},
		{
			name: "managedBy with wrong key",
			request: MutationRequest{
				Type:  MutationTypeManagedBy,
				Key:   "wrong-key",
				Value: "custom-controller.io",
			},
			expectErr: true,
			errMsg:    "managedBy mutation must use key",
		},
		{
			name: "valid multiKueue with queue",
			request: MutationRequest{
				Type:  MutationTypeMultiKueue,
				Key:   "managedBy",
				Value: "my-queue",
			},
			expectErr: false,
		},
		{
			name: "valid multiKueue without queue",
			request: MutationRequest{
				Type:  MutationTypeMultiKueue,
				Key:   "managedBy",
				Value: "",
			},
			expectErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)
			err := tt.request.Validate()

			if tt.expectErr {
				g.Expect(err).To(HaveOccurred())
				if tt.errMsg != "" {
					g.Expect(err.Error()).To(ContainSubstring(tt.errMsg))
				}
				return
			}

			g.Expect(err).NotTo(HaveOccurred())
		})
	}
}

func TestMutationRequest_Usage(t *testing.T) {
	g := NewWithT(t)

	// Test typical usage patterns for MutationRequest
	annotation := MutationRequest{
		Type:  MutationTypeAnnotation,
		Key:   "tekton.dev/pipeline",
		Value: "my-pipeline",
	}

	err := annotation.Validate()
	g.Expect(err).NotTo(HaveOccurred(), "Valid annotation should not produce error")

	label := MutationRequest{
		Type:  MutationTypeLabel,
		Key:   "environment",
		Value: "production",
	}

	err = label.Validate()
	g.Expect(err).NotTo(HaveOccurred(), "Valid label should not produce error")

	resource := MutationRequest{
		Type:  MutationTypeResource,
		Key:   "kueue.x-k8s.io/cpu-limit",
		Value: "1000",
	}

	err = resource.Validate()
	g.Expect(err).NotTo(HaveOccurred(), "Valid resource should not produce error")

	// Test JSON marshaling/unmarshaling
	data, err := json.Marshal(annotation)
	g.Expect(err).NotTo(HaveOccurred(), "Failed to marshal annotation")

	var unmarshaled MutationRequest
	err = json.Unmarshal(data, &unmarshaled)
	g.Expect(err).NotTo(HaveOccurred(), "Failed to unmarshal annotation")

	g.Expect(unmarshaled).To(Equal(annotation), "Unmarshaled annotation should match original")
}
