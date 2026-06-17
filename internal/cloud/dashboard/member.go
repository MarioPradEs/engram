package dashboard

// ProvisionedUser is a data-transfer type for a single entry in users.yaml as
// surfaced by the admin member-management UI. It mirrors users.Principal but
// lives in the dashboard package to avoid a circular import with cloudserver.
//
// Populated by the ListProvisionedUsers closure in MountConfig (D4).
type ProvisionedUser struct {
	Email      string
	Name       string
	Department string
	Role       string   // "admin" | "member"
	Status     string   // "active" | "offboarding" | "removed"
	Enrolled   []string // Enrolled project keys.
}
