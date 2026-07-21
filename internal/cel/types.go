package cel

import (
	"encoding/json"
	"fmt"
	"slices"
)

// MutationType represents the type of mutation to perform
type MutationType string

// Valid mutation types
const (
	MutationTypeAnnotation MutationType = "annotation"
	MutationTypeLabel      MutationType = "label"
	MutationTypeResource   MutationType = "resource"
	MutationTypeManagedBy  MutationType = "managedBy"
	MutationTypeMultiKueue MutationType = "multiKueue"
)

// IsValid checks if the mutation type is valid
func (mt MutationType) IsValid() bool {
	return slices.Contains(ValidTypes(), mt)
}

// String returns the string representation of the mutation type
func (mt MutationType) String() string {
	return string(mt)
}

// ValidTypes returns all valid mutation types
func ValidTypes() []MutationType {
	return []MutationType{MutationTypeAnnotation, MutationTypeLabel, MutationTypeResource, MutationTypeManagedBy, MutationTypeMultiKueue}
}

// UnmarshalJSON implements json.Unmarshaler interface with validation
func (mt *MutationType) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}

	mutationType := MutationType(s)
	if !mutationType.IsValid() {
		return fmt.Errorf("invalid mutation type: %q, must be one of: %v", s, ValidTypes())
	}

	*mt = mutationType
	return nil
}

// MarshalJSON implements json.Marshaler interface
func (mt MutationType) MarshalJSON() ([]byte, error) {
	return json.Marshal(string(mt))
}

// MutationRequest represents a single mutation operation with type safety
type MutationRequest struct {
	Type  MutationType `json:"type"`
	Key   string       `json:"key"`
	Value string       `json:"value"`
}

// Validate ensures the MutationRequest is valid
func (mr *MutationRequest) Validate() error {
	if !mr.Type.IsValid() {
		return fmt.Errorf("invalid mutation type: %v", mr.Type)
	}
	if mr.Key == "" {
		return fmt.Errorf("mutation key cannot be empty")
	}
	if mr.Value == "" && mr.Type != MutationTypeMultiKueue {
		return fmt.Errorf("mutation value cannot be empty")
	}
	if mr.Type == MutationTypeManagedBy && mr.Key != "managedBy" {
		return fmt.Errorf("managedBy mutation must use key %q, got %q", "managedBy", mr.Key)
	}
	return nil
}
