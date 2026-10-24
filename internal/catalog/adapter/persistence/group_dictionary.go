package persistence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	catalogdomain "bansoko/internal/catalog/domain"
	"github.com/google/uuid"
)

// groupDictionaryColumns is the projection every read of the dictionary's user
// layer shares, so a read and its scanner stay in one place. The row's id is
// deliberately absent: an entry's identity is the provider-namespaced group
// reference, and nothing above this boundary has a use for the surrogate key.
const groupDictionaryColumns = `provider, provider_group_ref, display_name, available, source, added_at`

// rowScanner is the shared shape of *sql.Row and *sql.Rows for one row.
type rowScanner interface {
	Scan(...any) error
}

// GroupDictionaryRepository is the persistence boundary for the user layer of
// the subgroup dictionary: the groups a user retrieved and added. The collected
// layer never reaches the database — it is embedded in the binary — so this
// repository only ever sees source='user' rows.
type GroupDictionaryRepository struct {
	db *sql.DB
}

func NewGroupDictionaryRepository(db *sql.DB) *GroupDictionaryRepository {
	return &GroupDictionaryRepository{db: db}
}

// ListGroupDictionaryEntries returns one provider's user-layer entries. It
// filters nothing: the caller owns the query rule, so the collected and stored
// halves of a search are matched by the same code.
func (r *GroupDictionaryRepository) ListGroupDictionaryEntries(ctx context.Context, provider string) ([]catalogdomain.GroupDictionaryEntry, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("subgroup group dictionary repository is nil")
	}
	rows, err := r.db.QueryContext(ctx, `SELECT `+groupDictionaryColumns+` FROM provider_group_dictionary WHERE provider = $1 ORDER BY added_at DESC, provider_group_ref`, provider)
	if err != nil {
		return nil, fmt.Errorf("list subgroup group dictionary: %w", err)
	}
	defer rows.Close()
	entries := make([]catalogdomain.GroupDictionaryEntry, 0)
	for rows.Next() {
		entry, err := scanGroupDictionaryEntry(rows)
		if err != nil {
			return nil, fmt.Errorf("scan subgroup group dictionary entry: %w", err)
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

// FindGroupDictionaryEntry resolves one exact key in the user layer.
func (r *GroupDictionaryRepository) FindGroupDictionaryEntry(ctx context.Context, provider, groupRef string) (catalogdomain.GroupDictionaryEntry, bool, error) {
	if r == nil || r.db == nil {
		return catalogdomain.GroupDictionaryEntry{}, false, errors.New("subgroup group dictionary repository is nil")
	}
	row := r.db.QueryRowContext(ctx, `SELECT `+groupDictionaryColumns+` FROM provider_group_dictionary WHERE provider = $1 AND provider_group_ref = $2`, provider, groupRef)
	entry, err := scanGroupDictionaryEntry(row)
	if errors.Is(err, sql.ErrNoRows) {
		return catalogdomain.GroupDictionaryEntry{}, false, nil
	}
	if err != nil {
		return catalogdomain.GroupDictionaryEntry{}, false, fmt.Errorf("read subgroup group dictionary entry: %w", err)
	}
	return entry, true, nil
}

// InsertGroupDictionaryEntry adds one retrieved group to the user layer and
// returns the stored entry.
//
// It is idempotent: the unique (provider, provider_group_ref) key makes a second
// insert of the same group a no-op, and the call then returns the row the first
// one stored. Two concurrent retrievals of one key therefore both succeed and
// leave exactly one row, which is what lets the retrieval endpoint repeat
// safely.
func (r *GroupDictionaryRepository) InsertGroupDictionaryEntry(ctx context.Context, provider, groupRef, displayName string, available bool) (catalogdomain.GroupDictionaryEntry, error) {
	if r == nil || r.db == nil {
		return catalogdomain.GroupDictionaryEntry{}, errors.New("subgroup group dictionary repository is nil")
	}
	row := r.db.QueryRowContext(ctx, `INSERT INTO provider_group_dictionary(id, provider, provider_group_ref, display_name, available, source)
VALUES ($1, $2, $3, $4, $5, 'user')
ON CONFLICT (provider, provider_group_ref) DO NOTHING
RETURNING `+groupDictionaryColumns, uuid.NewString(), provider, groupRef, displayName, available)
	entry, err := scanGroupDictionaryEntry(row)
	if errors.Is(err, sql.ErrNoRows) {
		entry, found, readErr := r.FindGroupDictionaryEntry(ctx, provider, groupRef)
		if readErr != nil {
			return catalogdomain.GroupDictionaryEntry{}, readErr
		}
		if !found {
			return catalogdomain.GroupDictionaryEntry{}, fmt.Errorf("subgroup group dictionary entry %s:%s conflicted but could not be read back", provider, groupRef)
		}
		return entry, nil
	}
	if err != nil {
		return catalogdomain.GroupDictionaryEntry{}, fmt.Errorf("insert subgroup group dictionary entry: %w", err)
	}
	return entry, nil
}

// DeleteGroupDictionaryEntry removes one entry from the user layer and reports
// whether a row was there. It never reports an error for an absent row: the
// caller decides what an absent row means, because the collected layer may
// still hold the entry.
func (r *GroupDictionaryRepository) DeleteGroupDictionaryEntry(ctx context.Context, provider, groupRef string) (bool, error) {
	if r == nil || r.db == nil {
		return false, errors.New("subgroup group dictionary repository is nil")
	}
	result, err := r.db.ExecContext(ctx, `DELETE FROM provider_group_dictionary WHERE provider = $1 AND provider_group_ref = $2`, provider, groupRef)
	if err != nil {
		return false, fmt.Errorf("delete subgroup group dictionary entry: %w", err)
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("read deleted subgroup group dictionary row count: %w", err)
	}
	return deleted > 0, nil
}

func scanGroupDictionaryEntry(row rowScanner) (catalogdomain.GroupDictionaryEntry, error) {
	var entry catalogdomain.GroupDictionaryEntry
	var source string
	if err := row.Scan(&entry.Provider, &entry.GroupRef, &entry.DisplayName, &entry.Available, &source, &entry.AddedAt); err != nil {
		return catalogdomain.GroupDictionaryEntry{}, err
	}
	entry.Source = catalogdomain.GroupDictionarySource(source)
	if err := entry.Validate(); err != nil {
		return catalogdomain.GroupDictionaryEntry{}, err
	}
	return entry, nil
}
