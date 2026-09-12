// Purpose: elevated, passphrase-wrapped whole-vault export and import.
// Inputs: a Broker, a non-empty passphrase, and for import an age envelope.
// Outputs: an authenticated age envelope or restored vault entries.
// Constraints: authorization precedes custody access; secret values never
// appear in errors, logs, names, or unencrypted return values.
// SPORT: internal.secrets.Broker.Export/ADD, Broker.Import/ADD (P1-E19-W4-S42-T3).

package secrets

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"io"

	"github.com/acamarata/cascade/pkg/cascade"
)

const (
	// VerbBackupExport is the elevated whole-vault read operation.
	VerbBackupExport = "backup.export"
	// VerbBackupImport is the elevated whole-vault replacement operation.
	VerbBackupImport = "backup.import"
)

// ExportRequest configures Broker.Export.
type ExportRequest struct {
	Passphrase string
}

// ImportRequest configures Broker.Import.
type ImportRequest struct {
	Wrapped    []byte
	Passphrase string
}

type vaultExportRecord struct {
	Name  string `json:"name"`
	Value []byte `json:"value"`
}

type vaultExportDocument struct {
	Version int                 `json:"version"`
	Entries []vaultExportRecord `json:"entries"`
}

// Export reads every value only after elevation and returns age ciphertext.
func (b *Broker) Export(ctx context.Context, req ExportRequest) ([]byte, error) {
	if req.Passphrase == "" {
		return nil, cascade.New(cascade.KindInvalidInput, "secrets: backup export requires a passphrase")
	}
	if err := b.authorize(ctx, VerbBackupExport); err != nil {
		return nil, err
	}
	doc, err := b.readExportDocument(ctx)
	if err != nil {
		return nil, err
	}
	plain, err := json.Marshal(doc)
	zeroExportDocument(doc)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindInternal, err, "secrets: encode backup export")
	}
	defer zero(plain)
	wrapped, err := ageEncrypt(req.Passphrase, plain, ageDefaultLogN, rand.Reader)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "secrets: wrap backup export")
	}
	return wrapped, nil
}

func (b *Broker) readExportDocument(ctx context.Context) (vaultExportDocument, error) {
	names, err := b.custody.List(ctx)
	if err != nil {
		return vaultExportDocument{}, err
	}
	doc := vaultExportDocument{Version: 1, Entries: make([]vaultExportRecord, 0, len(names))}
	for _, name := range names {
		value, gerr := b.custody.Get(ctx, name)
		if gerr != nil {
			zeroExportDocument(doc)
			return vaultExportDocument{}, gerr
		}
		doc.Entries = append(doc.Entries, vaultExportRecord{Name: name, Value: value})
	}
	return doc, nil
}

// Import authenticates and decodes a whole-vault envelope before writing.
func (b *Broker) Import(ctx context.Context, req ImportRequest) error {
	if req.Passphrase == "" || len(req.Wrapped) == 0 {
		return cascade.New(cascade.KindInvalidInput, "secrets: backup import requires an envelope and passphrase")
	}
	if err := b.authorize(ctx, VerbBackupImport); err != nil {
		return err
	}
	plain, err := ageDecrypt(req.Passphrase, req.Wrapped)
	if err != nil {
		return cascade.Wrap(cascade.KindIntegrity, err, "secrets: unwrap backup import")
	}
	defer zero(plain)
	doc, err := decodeVaultExport(plain)
	if err != nil {
		return err
	}
	defer zeroExportDocument(doc)
	for _, entry := range doc.Entries {
		if _, err := b.Set(ctx, entry.Name, entry.Value, SetUpdate); err != nil {
			return err
		}
	}
	return nil
}

func decodeVaultExport(data []byte) (vaultExportDocument, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var doc vaultExportDocument
	if err := dec.Decode(&doc); err != nil {
		return vaultExportDocument{}, cascade.Wrap(cascade.KindIntegrity, err, "secrets: invalid backup export")
	}
	if doc.Version != 1 {
		return vaultExportDocument{}, cascade.Newf(cascade.KindUnsupported,
			"secrets: backup export version %d is unsupported", doc.Version)
	}
	if err := rejectTrailingJSON(dec); err != nil {
		return vaultExportDocument{}, err
	}
	for _, entry := range doc.Entries {
		if err := validateSecretName(entry.Name); err != nil {
			return vaultExportDocument{}, err
		}
	}
	return doc, nil
}

func rejectTrailingJSON(dec *json.Decoder) error {
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return cascade.New(cascade.KindIntegrity, "secrets: backup export has trailing data")
		}
		return cascade.Wrap(cascade.KindIntegrity, err, "secrets: invalid backup export trailer")
	}
	return nil
}

func zeroExportDocument(doc vaultExportDocument) {
	for i := range doc.Entries {
		zero(doc.Entries[i].Value)
	}
}
