package application

import (
	"bansoko/internal/persistence/txscope"
	providerdomain "bansoko/internal/provider/domain"
	selectiondomain "bansoko/internal/selection/domain"
	"context"
	"testing"
)

type unionPolicyReader struct {
	policy selectiondomain.DiscoveryPolicy
}

func (r unionPolicyReader) ReadDiscoveryPolicy(context.Context, string) (selectiondomain.DiscoveryPolicy, error) {
	return r.policy, nil
}
func (r unionPolicyReader) ReadDiscoveryPolicyInTx(context.Context, txscope.Tx, string) (selectiondomain.DiscoveryPolicy, error) {
	return r.policy, nil
}
func TestManualRecomputeUsesAvailableUnion(t *testing.T) {
	db := openDispatcherTestDB(t)
	insertProviderSubscription(t, db)
	ctx := context.Background()
	if _, err := db.Exec(`INSERT INTO provider_group_selections(id,subscription_id,provider,group_ref,display_name,selection_mode,manual_selected,effective_selected,effective_score,count_in_limit,available) VALUES('old','sub-1','anibt','old','Old','auto',NULL,TRUE,50,TRUE,TRUE),('new','sub-1','anibt','new','New','auto',NULL,TRUE,50,TRUE,TRUE),('manual','sub-1','anibt','manual','Manual','manual',TRUE,TRUE,0,TRUE,TRUE),('retired','sub-1','anibt','retired','Retired','manual',TRUE,TRUE,0,FALSE,FALSE)`); err != nil {
		t.Fatal(err)
	}
	d := Dispatcher{DB: db, PolicyReader: unionPolicyReader{selectiondomain.DiscoveryPolicy{MaxRSS: 1, ProviderDefaults: map[providerdomain.Kind]selectiondomain.ProviderDefaultPolicy{providerdomain.ProviderAniBT: {Mode: selectiondomain.Ranked, Score: 50}}}}}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	check := func(want map[string]bool) {
		t.Helper()
		for id, expected := range want {
			var selected bool
			if err := tx.QueryRow(`SELECT effective_selected FROM provider_group_selections WHERE id=$1`, id).Scan(&selected); err != nil {
				t.Fatal(err)
			}
			if selected != expected {
				t.Fatalf("%s selected=%t want=%t", id, selected, expected)
			}
		}
	}
	if err := d.RecomputeSubscriptionSelections(ctx, txscope.Wrap(tx), "sub-1", "anime-1"); err != nil {
		t.Fatal(err)
	}
	check(map[string]bool{"old": false, "new": false, "manual": true, "retired": false})
	if _, err := tx.Exec(`UPDATE provider_group_selections SET count_in_limit=FALSE WHERE id='manual'`); err != nil {
		t.Fatal(err)
	}
	if err := d.RecomputeSubscriptionSelections(ctx, txscope.Wrap(tx), "sub-1", "anime-1"); err != nil {
		t.Fatal(err)
	}
	check(map[string]bool{"old": false, "new": true, "manual": true, "retired": false})
}
