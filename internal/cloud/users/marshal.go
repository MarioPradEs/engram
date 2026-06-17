package users

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// MarshalPrincipals serializes a slice of Principal values to users.yaml format
// and validates the result before returning. This ensures the serialized bytes
// will pass loadAndValidate when used with WriteAtomic.
func MarshalPrincipals(principals []Principal) ([]byte, error) {
	users := make([]yamlUser, 0, len(principals))
	for _, p := range principals {
		users = append(users, yamlUser{
			Email:      p.Email,
			Name:       p.Name,
			Department: p.Department,
			Role:       p.Role,
			Enrolled:   p.Enrolled,
			Status:     p.Status,
		})
	}
	f := yamlFile{Users: users}
	data, err := yaml.Marshal(&f)
	if err != nil {
		return nil, fmt.Errorf("users: marshal principals: %w", err)
	}
	return data, nil
}

// ValidatePrincipal performs the same per-entry validation as loadAndValidate
// for a single entry. Returns an error describing the first invalid field.
func ValidatePrincipal(p Principal) error {
	email := strings.ToLower(strings.TrimSpace(p.Email))
	if email == "" {
		return fmt.Errorf("email is required")
	}
	if !strings.HasSuffix(email, "@vivastudios.com") {
		return fmt.Errorf("email %q must end with @vivastudios.com", email)
	}
	dept := strings.ToLower(strings.TrimSpace(p.Department))
	if !validDepartments[dept] {
		return fmt.Errorf("invalid department %q (valid: %s)", p.Department, joinKeys(validDepartments))
	}
	role := strings.ToLower(strings.TrimSpace(p.Role))
	if !validRoles[role] {
		return fmt.Errorf("invalid role %q (valid: admin, member)", p.Role)
	}
	status := strings.ToLower(strings.TrimSpace(p.Status))
	if !validStatuses[status] {
		return fmt.Errorf("invalid status %q (valid: active, offboarding, removed)", p.Status)
	}
	return nil
}

// ValidatorForPath returns a validator function suitable for use with WriteAtomic.
// It parses and validates the YAML at the given temp-file path using loadAndValidate.
func ValidatorForPath() func(string) error {
	return func(tmpPath string) error {
		_, err := loadAndValidate(tmpPath)
		return err
	}
}
