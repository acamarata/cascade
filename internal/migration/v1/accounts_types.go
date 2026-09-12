// Package v1 uses this file for strict accounts.json wire types.
// Purpose: decode and validate the complete known schema-version 1 shape.
// Inputs: archived schema-version 1 JSON.
// Outputs: a fully decoded registry or a fail-closed typed refusal.
// Constraints: every known v1 field is represented; unknown fields and enums
// are errors, and required zero-valued scalars use pointers to detect absence.
// SPORT: migration/v1/accounts/ADD (P1-E26-W10-S53-T1).
package v1

import (
	"bytes"
	"encoding/json"
	"io"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

type v1AccountsRegistry struct {
	SchemaVersion int             `json:"schema_version"`
	UpdatedAt     string          `json:"updated_at"`
	Accounts      []v1Account     `json:"accounts"`
	ModelMatrix   []v1ModelMatrix `json:"model_matrix"`
}

type v1Account struct {
	ID                 string   `json:"id"`
	Family             string   `json:"family"`
	Subscription       string   `json:"subscription"`
	AccessMethods      []string `json:"access_methods"`
	Role               string   `json:"role"`
	ExhaustionPriority *uint8   `json:"exhaustion_priority"`
	Models             []string `json:"models"`
	CLIAvailable       *bool    `json:"cli_available"`
	KeyCount           *uint32  `json:"key_count"`
	QuotaAccountID     *string  `json:"quota_account_id,omitempty"`
	Notes              *string  `json:"notes,omitempty"`
}

type v1ModelRoute struct {
	AccountID string `json:"account_id"`
	Method    string `json:"method"`
}

type v1ModelMatrix struct {
	ModelID       string         `json:"model_id"`
	AvailableVia  []v1ModelRoute `json:"available_via"`
	BestFor       []string       `json:"best_for"`
	Tier          string         `json:"tier"`
	Family        string         `json:"family"`
	Notes         *string        `json:"notes,omitempty"`
	BestForLabels []string       `json:"best_for_labels,omitempty"`
	ContextTokens *uint64        `json:"context_tokens,omitempty"`
	MaxOutput     *uint64        `json:"max_output_tokens,omitempty"`
	Availability  *string        `json:"availability,omitempty"`
}

func decodeV1Accounts(data []byte) (v1AccountsRegistry, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var registry v1AccountsRegistry
	if err := dec.Decode(&registry); err != nil {
		return v1AccountsRegistry{}, cascade.Wrap(cascade.KindIntegrity, err,
			"migration v1 accounts: decode schema-version 1 registry")
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return v1AccountsRegistry{}, cascade.Wrap(cascade.KindIntegrity, ErrMalformedInput,
			"migration v1 accounts: trailing JSON data")
	}
	if registry.SchemaVersion != 1 {
		return v1AccountsRegistry{}, cascade.Wrapf(cascade.KindUnsupported, ErrVersionMismatch,
			"migration v1 accounts: schema_version %d is not supported", registry.SchemaVersion)
	}
	if _, err := time.Parse(time.RFC3339, registry.UpdatedAt); err != nil {
		return v1AccountsRegistry{}, cascade.Wrap(cascade.KindIntegrity, err,
			"migration v1 accounts: updated_at is not RFC3339")
	}
	return registry, validateV1Accounts(registry)
}

func validateV1Accounts(reg v1AccountsRegistry) error {
	ids := make(map[string]bool, len(reg.Accounts))
	for _, account := range reg.Accounts {
		if err := validateV1Account(account, ids); err != nil {
			return err
		}
		ids[account.ID] = true
	}
	for _, matrix := range reg.ModelMatrix {
		if err := validateV1Matrix(matrix, ids); err != nil {
			return err
		}
	}
	return nil
}

func validateV1Account(account v1Account, ids map[string]bool) error {
	if account.ID == "" || ids[account.ID] {
		return cascade.Wrapf(cascade.KindInvalidInput, ErrUnknownInput,
			"migration v1 accounts: missing or duplicate account id %q", account.ID)
	}
	if _, ok := mapV1Driver(account.Family); !ok {
		return unknownAccountEnum(account.ID, "family", account.Family)
	}
	if _, _, ok := mapV1Role(account.Role); !ok {
		return unknownAccountEnum(account.ID, "role", account.Role)
	}
	if account.ExhaustionPriority == nil || account.CLIAvailable == nil || account.KeyCount == nil {
		return cascade.Wrapf(cascade.KindIntegrity, ErrMalformedInput,
			"migration v1 accounts: account %q is missing required scalar metadata", account.ID)
	}
	if len(account.AccessMethods) == 0 {
		return cascade.Wrapf(cascade.KindIntegrity, ErrMalformedInput,
			"migration v1 accounts: account %q has no access method", account.ID)
	}
	for _, method := range account.AccessMethods {
		if !knownAccessMethod(method) {
			return unknownAccountEnum(account.ID, "access method", method)
		}
	}
	return nil
}

func validateV1Matrix(matrix v1ModelMatrix, ids map[string]bool) error {
	if matrix.ModelID == "" || matrix.Family == "" || matrix.Tier == "" {
		return cascade.Wrap(cascade.KindIntegrity, ErrMalformedInput,
			"migration v1 accounts: model_matrix entry is missing identity metadata")
	}
	if _, ok := mapV1Driver(matrix.Family); !ok {
		return unknownAccountEnum(matrix.ModelID, "matrix family", matrix.Family)
	}
	for _, route := range matrix.AvailableVia {
		if !ids[route.AccountID] || !knownAccessMethod(route.Method) {
			return cascade.Wrapf(cascade.KindInvalidInput, ErrUnknownInput,
				"migration v1 accounts: model %q has an unknown route", matrix.ModelID)
		}
	}
	for _, class := range matrix.BestFor {
		if !knownTaskClass(class) {
			return unknownAccountEnum(matrix.ModelID, "task class", class)
		}
	}
	return nil
}

func unknownAccountEnum(id, field, value string) error {
	return cascade.Wrapf(cascade.KindInvalidInput, ErrUnknownInput,
		"migration v1 accounts: %s %q has unknown %s %q", "record", id, field, value)
}
