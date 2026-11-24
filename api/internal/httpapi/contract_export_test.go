package httpapi

import "testing"

// ContractValidator exposes the contract-driven response check to the
// httpapi_test package, so an integration test of a new endpoint can validate
// its real responses against api/openapi/openapi.yaml without joining the big
// conformance scenario.
type ContractValidator struct{ v *contractValidator }

// NewContractValidator loads the OpenAPI contract.
func NewContractValidator(t *testing.T) *ContractValidator {
	t.Helper()
	return &ContractValidator{v: newContractValidator(t)}
}

// AssertResponse validates body against the schema declared for exactly this
// (path template, method, status).
func (c *ContractValidator) AssertResponse(t *testing.T, pathTemplate, method string, status int, body []byte) {
	t.Helper()
	c.v.assertResponse(t, pathTemplate, method, status, body)
}
