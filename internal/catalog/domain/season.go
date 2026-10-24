package domain

import (
	"fmt"
	"time"
)

// seasonTerms mirrors frontend/src/features/catalog/season.ts: canonical
// seasons are `YYYY-term` with winter=Jan-Mar, spring=Apr-Jun, summer=Jul-Sep
// and fall=Oct-Dec.
var seasonTerms = [4]string{"winter", "spring", "summer", "fall"}

// SeasonAt returns the canonical catalog season containing t, read in t's
// location.
func SeasonAt(t time.Time) string {
	return fmt.Sprintf("%d-%s", t.Year(), seasonTerms[(int(t.Month())-1)/3])
}

// SeasonAfter returns the canonical catalog season that follows the one
// containing t.
func SeasonAfter(t time.Time) string {
	index := t.Year()*4 + (int(t.Month())-1)/3 + 1
	return fmt.Sprintf("%d-%s", index/4, seasonTerms[index%4])
}

// SeasonBefore returns the canonical catalog season that precedes the one
// containing t.
func SeasonBefore(t time.Time) string {
	index := t.Year()*4 + (int(t.Month())-1)/3 - 1
	return fmt.Sprintf("%d-%s", index/4, seasonTerms[index%4])
}
