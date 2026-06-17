package cloudserver

import (
	"os"

	"github.com/Gentleman-Programming/engram/internal/cloud/dashboard"
	"github.com/Gentleman-Programming/engram/internal/cloud/users"
)

// listableUserDirectory is satisfied by *users.YAMLLoader when it has a List()
// method. Using a structural interface keeps the AuthUserLoader interface stable.
type listableUserDirectory interface {
	List() []users.Principal
}

// resolveUsersFilePath returns the users.yaml path for the admin member-management
// handlers. Prefers the value set via WithUsersFilePath; falls back to ENGRAM_USERS_FILE.
func (s *CloudServer) resolveUsersFilePath() string {
	if s.usersFilePath != "" {
		return s.usersFilePath
	}
	return os.Getenv("ENGRAM_USERS_FILE")
}

// listProvisionedUsersFunc returns a closure that converts users.Principal
// slices from YAMLLoader.List() into dashboard.ProvisionedUser slices.
// Returns nil when authLoader does not implement listableUserDirectory
// (e.g. legacy bearer-token deployments without a users.yaml).
func (s *CloudServer) listProvisionedUsersFunc() func() []dashboard.ProvisionedUser {
	lister, ok := s.authLoader.(listableUserDirectory)
	if !ok {
		return nil
	}
	return func() []dashboard.ProvisionedUser {
		principals := lister.List()
		out := make([]dashboard.ProvisionedUser, 0, len(principals))
		for _, p := range principals {
			out = append(out, dashboard.ProvisionedUser{
				Email:      p.Email,
				Name:       p.Name,
				Department: p.Department,
				Role:       p.Role,
				Status:     p.Status,
				Enrolled:   p.Enrolled,
			})
		}
		return out
	}
}
