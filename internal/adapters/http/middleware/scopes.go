package middleware

// The v1 route scope map required by the approved plan.
const (
	ScopeWalletsWrite          = "wallets:write"
	ScopeWalletsRead           = "wallets:read"
	ScopeReconciliationExecute = "reconciliation:execute"
	ScopeMetricsRead           = "metrics:read"
	ScopeWageringWrite         = "wagering:write"
	ScopeWageringRead          = "wagering:read"
)

// RouteScopes maps every supplied v1 route to its exact required scope.
var RouteScopes = map[string]string{
	"POST /wallets":                              ScopeWalletsWrite,
	"GET /wallets/{walletId}":                    ScopeWalletsRead,
	"GET /wallets/{walletId}/ledger":             ScopeWalletsRead,
	"POST /wallets/{walletId}/reconciliation":    ScopeReconciliationExecute,
	"GET /metrics":                               ScopeMetricsRead,
	"POST /wagering/transactions":                ScopeWageringWrite,
	"GET /wagering/transactions/{transactionId}": ScopeWageringRead,
	"GET /providers/{providerId}/wagering/transactions/{externalTransactionId}": ScopeWageringRead,
}
