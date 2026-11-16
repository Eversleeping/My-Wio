// Package protocol defines the persisted Agent wire contract.
//
// To add an operation, add a kind constant and its kinds.golden entry, define
// its payload/result DTOs, register them in wirecontract, and capture a fixture
// from the actual producer. Then register the Agent handler and, when needed,
// a capability name. Existing wire values and JSON field names must not change.
//
// Wire fixtures in testdata/wire are append-only: retain old samples unchanged
// when introducing a compatible format variant. Never sample production secrets
// or machine configuration; use isolated tests and normalize incidental values.
package protocol
