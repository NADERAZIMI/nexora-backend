package identity

import "testing"

// TestReferencePermissionsAreAdministrative proves that reference-data
// authoring, approval, publication, and activation are not granted to customer
// or read-only roles.
func TestReferencePermissionsAreAdministrative(t *testing.T) {
	admin := PermissionsFor([]Role{RoleAdmin})
	for _, permission := range []string{
		PermAdminReferenceRead, PermAdminReferenceWrite, PermAdminReferenceApprove,
		PermAdminReferencePublish, PermAdminReferenceActivate,
	} {
		if !containsPermission(admin, permission) {
			t.Fatalf("admin role is missing %s", permission)
		}
	}
	for _, role := range []Role{RoleCustomer, RoleSupport, RoleCompliance, RoleTreasury} {
		for _, permission := range []string{
			PermAdminReferenceWrite, PermAdminReferenceApprove,
			PermAdminReferencePublish, PermAdminReferenceActivate,
		} {
			if containsPermission(PermissionsFor([]Role{role}), permission) {
				t.Fatalf("role %s unexpectedly holds %s", role, permission)
			}
		}
	}
	auditor := PermissionsFor([]Role{RoleAuditor})
	if !containsPermission(auditor, PermAdminReferenceRead) {
		t.Fatal("auditor role must retain read-only reference access")
	}
	if containsPermission(auditor, PermAdminReferenceWrite) || containsPermission(auditor, PermAdminReferencePublish) {
		t.Fatal("auditor role must not mutate or publish reference data")
	}
}

func containsPermission(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
