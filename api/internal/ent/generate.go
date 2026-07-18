// Package ent holds the generated data layer. Regenerate with `go generate ./internal/ent`.
package ent

//go:generate go run -mod=mod entgo.io/ent/cmd/ent generate --feature intercept,sql/upsert,sql/lock,sql/modifier,sql/execquery,sql/versioned-migration ./schema
