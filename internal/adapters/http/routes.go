package http

// routeCatalog is the implicit v1 HTTP contract. Supplied routes stay
// unprefixed; an incompatible change must use a new route or media type.
//
// GET /health/live
// GET /health/ready
// GET /metrics
// POST /wallets
// GET /wallets/{walletId}
// GET /wallets/{walletId}/ledger
// GET /wagering/transactions/{transactionId}
// GET /providers/{providerId}/wagering/transactions/{externalTransactionId}
// POST /wagering/transactions
// POST /wallets/{walletId}/reconciliation
const routeCatalog = `
GET /health/live
GET /health/ready
GET /metrics
POST /wallets
GET /wallets/{walletId}
GET /wallets/{walletId}/ledger
GET /wagering/transactions/{transactionId}
GET /providers/{providerId}/wagering/transactions/{externalTransactionId}
POST /wagering/transactions
POST /wallets/{walletId}/reconciliation
`

// Route is one entry of the v1 route catalog.
type Route struct {
	Method string
	Path   string
}

// Routes returns the supplied v1 routes in catalog order.
func Routes() []Route {
	return []Route{
		{Method: "GET", Path: "/health/live"},
		{Method: "GET", Path: "/health/ready"},
		{Method: "GET", Path: "/metrics"},
		{Method: "POST", Path: "/wallets"},
		{Method: "GET", Path: "/wallets/{walletId}"},
		{Method: "GET", Path: "/wallets/{walletId}/ledger"},
		{Method: "GET", Path: "/wagering/transactions/{transactionId}"},
		{Method: "GET", Path: "/providers/{providerId}/wagering/transactions/{externalTransactionId}"},
		{Method: "POST", Path: "/wagering/transactions"},
		{Method: "POST", Path: "/wallets/{walletId}/reconciliation"},
	}
}
