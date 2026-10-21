package identity

// Permission names are stable, server-side authorization identifiers. The
// default is deny: unknown roles and permissions grant nothing.
const (
	PermSelfRead           = "self:read"
	PermSelfWrite          = "self:write"
	PermAPIKeyManage       = "api_key:manage"
	PermKYCSubmit          = "kyc:submit"
	PermAdminUserRead      = "admin:user:read"
	PermAdminUserFreeze    = "admin:user:freeze"
	PermAdminRoleManage    = "admin:role:manage"
	PermAdminSessionRevoke = "admin:session:revoke"
	PermAdminAPIKeyRevoke  = "admin:apikey:revoke" //nolint:gosec // stable permission name, not a credential
	PermAdminAuditRead     = "admin:audit:read"
	PermAdminKYCRead       = "admin:kyc:read"
	PermAdminKYCDecide     = "admin:kyc:decide"
)

// rolePermissions is intentionally explicit. Customer roles never receive an
// admin:* permission, and administrative roles never receive customer-only
// permissions such as api_key:manage.
var rolePermissions = map[Role]map[string]struct{}{
	RoleCustomer: setOf(PermSelfRead, PermSelfWrite, PermAPIKeyManage, PermKYCSubmit),
	RoleSupport:  setOf(PermAdminUserRead, PermAdminAuditRead),
	RoleRiskOperator: setOf(
		PermAdminUserRead, PermAdminUserFreeze, PermAdminSessionRevoke,
		PermAdminAPIKeyRevoke, PermAdminAuditRead,
	),
	RoleCompliance: setOf(PermAdminKYCRead, PermAdminKYCDecide, PermAdminUserRead),
	RoleTreasury:   setOf(PermAdminUserRead),
	RoleAdmin: setOf(
		PermAdminUserRead, PermAdminUserFreeze, PermAdminRoleManage,
		PermAdminSessionRevoke, PermAdminAPIKeyRevoke, PermAdminAuditRead,
		PermAdminKYCRead, PermAdminKYCDecide,
	),
	RoleAuditor: setOf(PermAdminUserRead, PermAdminAuditRead, PermAdminKYCRead),
}

func setOf(values ...string) map[string]struct{} {
	out := make(map[string]struct{}, len(values))
	for _, value := range values {
		out[value] = struct{}{}
	}
	return out
}

func roleHasPermission(role Role, permission string) bool {
	if permission == "" {
		return false
	}
	granted, ok := rolePermissions[role]
	if !ok {
		return false
	}
	_, ok = granted[permission]
	return ok
}

// PermissionsFor returns a stable, sorted permission list for a role set.
func PermissionsFor(roles []Role) []string {
	seen := map[string]struct{}{}
	for _, role := range roles {
		for permission := range rolePermissions[role] {
			seen[permission] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for permission := range seen {
		out = append(out, permission)
	}
	sortStrings(out)
	return out
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}
