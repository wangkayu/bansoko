package application

import (
	"encoding/json"
	"errors"
	"math/rand"
	"testing"

	protocol "bansoko/internal/acquisition/terminal/domain"
	anime "bansoko/internal/anime/domain"
	catalog "bansoko/internal/catalog/domain"
	selectionapp "bansoko/internal/selection/application"
	selection "bansoko/internal/selection/domain"
)

// Deliberately fixture-only capacities, not defaults for the product.
func testWitnessLimits() witnessLimits { return witnessLimits{bytes: 200000, unionRows: 50} }

func witnessFixture(t *testing.T) qualificationMaterial {
	t.Helper()
	m := qualificationFixture(t, "automatic_source")
	yes, no := true, false
	p := anime.SubgroupsSection{MaxRss: 1, Entries: []anime.SubgroupScoreEntry{
		{Provider: "mikan", GroupRef: "1", DisplayName: "always", SelectionMode: "manual", ManualSelected: &yes, Score: 70},
		{Provider: "mikan", GroupRef: "2", DisplayName: "always free", SelectionMode: "manual", ManualSelected: &yes},
		{Provider: "mikan", GroupRef: "9", DisplayName: "never", SelectionMode: "manual", ManualSelected: &no},
		{Provider: "mikan", GroupRef: "99", DisplayName: "missing intent", SelectionMode: "manual", ManualSelected: &yes},
	}}
	rules := []anime.NameKeywordRule{{Keyword: "zero", Score: -50}, {Keyword: "negative", Score: -51}}
	p.NameKeywords = &anime.ProviderNameKeywordRules{Mikan: &rules}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	m.config.business.Sections[anime.SectionSubgroups] = raw
	m.config.digest, err = m.config.business.Digest()
	if err != nil {
		t.Fatal(err)
	}
	m.catalog.observation.Groups = nil
	for _, g := range []struct {
		ref, name string
		available bool
	}{{"1", "always", true}, {"10", "tie", true}, {"2", "always free", true}, {"20", "tie", true}, {"3", "zero", true}, {"4", "negative", true}, {"5", "inactive", false}, {"9", "never", true}} {
		available := g.available
		m.catalog.observation.Groups = append(m.catalog.observation.Groups, catalog.ObservedGroup{GroupRef: g.ref, DisplayName: g.name, Available: catalog.ObservationValue[bool]{State: "known", Value: &available}, ObservedAt: m.catalog.observation.ObservedAt})
	}
	return m
}

func decodeWitness(t *testing.T, w preparationWitness) witnessValue {
	t.Helper()
	var value witnessValue
	if err := json.Unmarshal([]byte(w.canonical), &value); err != nil {
		t.Fatal(err)
	}
	if protocol.Digest(witnessDigestDomain, []byte(w.canonical)) != w.digest {
		t.Fatal("witness digest mismatch")
	}
	return value
}

func TestPreparationWitnessWholeRanking(t *testing.T) {
	m := witnessFixture(t)
	c, err := constructQualification(m)
	if err != nil {
		t.Fatal(err)
	}
	input := catalog.DigestBytes([]byte("frozen admission input"))
	w, err := constructWitness(c, input, testWitnessLimits())
	if err != nil {
		t.Fatal(err)
	}
	value := decodeWitness(t, w)
	expected := []struct {
		ref, reason string
		rank        int
	}{{"1", "policy_always", 0}, {"2", "policy_always", 0}, {"10", "ranked_within_max_rss", 1}, {"20", "max_rss_exhausted", 2}, {"3", "max_rss_exhausted", 3}, {"4", "policy_score_negative", 0}, {"9", "policy_never", 0}, {"99", "provider_group_not_observed", 0}, {"5", "provider_group_not_observed", 0}}
	if len(value.Decisions) != len(expected) {
		t.Fatalf("union: %d", len(value.Decisions))
	}
	for i, want := range expected {
		got := value.Decisions[i]
		rank := 0
		if got.Rank != nil {
			rank = *got.Rank
		}
		if got.GroupRef != want.ref || got.ReasonCode != want.reason || rank != want.rank {
			t.Fatalf("decision %d: %+v want %+v", i, got, want)
		}
		if got.CanonicalKey.State != "absent" {
			t.Fatal("synthetic translation")
		}
	}
	if value.Decisions[0].Score != 100 || value.Decisions[1].Score != 100 {
		t.Fatal("required groups are written at the fixed maximum score")
	}
	if value.Decisions[4].Score != 0 || value.Decisions[5].Score != -1 {
		t.Fatal("resolved zero/negative score changed")
	}
	if string(mustCanonical(value.SelectedRefs)) != `["1","10","2"]` {
		t.Fatalf("selected: %v", value.SelectedRefs)
	}
	if value.Decisions[7].DisplayName.Value == nil || *value.Decisions[7].DisplayName.Value != "missing intent" {
		t.Fatal("unobserved intent name")
	}
	if value.PolicyDigest != protocol.Digest(policyDigestDomain, []byte(c.policyCanonical)) || value.ObservationDigest != protocol.Digest(catalog.GroupObservationDigestDomain, []byte(c.observationCanonical)) {
		t.Fatal("material digest domains")
	}
	var p anime.SubgroupsSection
	if err := json.Unmarshal(value.Policy, &p); err != nil {
		t.Fatal(err)
	}
	if p.Entries[0].EffectiveSelected {
		t.Fatal("effective_selected rewritten")
	}
	// The canonical observation itself must retain C order. Shuffle the typed
	// union sent to the existing core to prove its permutation invariance.
	policy, err := selectionapp.DiscoveryPolicyFromSubgroupsSection([]byte(c.policyCanonical), c.policyVersion)
	if err != nil {
		t.Fatal(err)
	}
	union := make([]selection.ObservedGroup, 0, len(value.Decisions))
	for _, d := range value.Decisions {
		name := ""
		if d.DisplayName.Value != nil {
			name = *d.DisplayName.Value
		}
		union = append(union, selection.ObservedGroup{ExternalID: d.GroupRef, Name: name, Available: d.Available})
	}
	baseline, err := selection.RankObservedGroups(m.catalog.observation.Scope.Provider, union, policy)
	if err != nil {
		t.Fatal(err)
	}
	rng := rand.New(rand.NewSource(199))
	for n := 0; n < 100; n++ {
		rng.Shuffle(len(union), func(i, j int) { union[i], union[j] = union[j], union[i] })
		got, err := selection.RankObservedGroups(m.catalog.observation.Scope.Provider, union, policy)
		if err != nil || string(mustCanonical(got)) != string(mustCanonical(baseline)) {
			t.Fatalf("union permutation: %v", err)
		}
	}
	for n := 0; n < 30; n++ {
		again, err := constructWitness(c, input, testWitnessLimits())
		if err != nil || again != w {
			t.Fatalf("unstable witness: %v", err)
		}
	}
	m.catalog.observation.Groups[0].DisplayName = "changed"
	if again, err := constructWitness(c, input, testWitnessLimits()); err != nil || again != w {
		t.Fatal("capture aliases caller")
	}
}

func TestPreparationWitnessPermutationAndNegativeRemaining(t *testing.T) {
	rng := rand.New(rand.NewSource(417))
	for _, maxRSS := range []int{0, 1, 2, 3} {
		m := witnessFixture(t)
		var p anime.SubgroupsSection
		if err := json.Unmarshal(m.config.business.Sections[anime.SectionSubgroups], &p); err != nil {
			t.Fatal(err)
		}
		p.MaxRss = maxRSS
		raw, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		m.config.business.Sections[anime.SectionSubgroups] = raw
		m.config.digest, err = m.config.business.Digest()
		if err != nil {
			t.Fatal(err)
		}
		c, err := constructQualification(m)
		if err != nil {
			t.Fatal(err)
		}
		input := catalog.DigestBytes([]byte("input"))
		baseline, err := constructWitness(c, input, testWitnessLimits())
		if err != nil {
			t.Fatal(err)
		}
		v := decodeWitness(t, baseline)
		for _, d := range v.Decisions {
			if d.GroupRef == "10" && maxRSS == 0 && d.ReasonCode != "max_rss_exhausted" {
				t.Fatal("zero max_rss still selected a ranked group")
			}
		}
		// Shuffle explicit rules while retaining the policy's own original order in
		// its digest. Ranking and selected sets must be independent of that order.
		for iteration := 0; iteration < 40; iteration++ {
			rng.Shuffle(len(p.Entries), func(i, j int) { p.Entries[i], p.Entries[j] = p.Entries[j], p.Entries[i] })
			raw, err = json.Marshal(p)
			if err != nil {
				t.Fatal(err)
			}
			m.config.business.Sections[anime.SectionSubgroups] = raw
			m.config.digest, err = m.config.business.Digest()
			if err != nil {
				t.Fatal(err)
			}
			c, err = constructQualification(m)
			if err != nil {
				t.Fatal(err)
			}
			w, err := constructWitness(c, input, testWitnessLimits())
			if err != nil {
				t.Fatal(err)
			}
			got := decodeWitness(t, w)
			if string(mustCanonical(got.Decisions)) != string(mustCanonical(v.Decisions)) || string(mustCanonical(got.SelectedRefs)) != string(mustCanonical(v.SelectedRefs)) {
				t.Fatal("rule permutation changed ranking")
			}
		}
	}
}

func TestPreparationWitnessLimitsAndMaterialIdentity(t *testing.T) {
	c, err := constructQualification(witnessFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	input := catalog.DigestBytes([]byte("input"))
	w, err := constructWitness(c, input, testWitnessLimits())
	if err != nil {
		t.Fatal(err)
	}
	rows := len(decodeWitness(t, w).Decisions)
	for _, limits := range []witnessLimits{{}, {bytes: len(w.canonical) - 1, unionRows: rows}, {bytes: len(w.canonical), unionRows: rows - 1}} {
		if _, err := constructWitness(c, input, limits); !errors.Is(err, errWitnessLimit) {
			t.Fatalf("limit not enforced: %v", err)
		}
	}
	exact, err := constructWitness(c, input, witnessLimits{bytes: len(w.canonical), unionRows: rows})
	if err != nil || exact != w {
		t.Fatalf("inclusive boundary: %v", err)
	}
	for _, change := range []func(*admissionCapture){func(c *admissionCapture) {
		c.policyCanonical = `{"entries":[],"max_rss":0,"name_keywords":null,"provider_defaults":null}`
	}, func(c *admissionCapture) { c.configDigest = input }, func(c *admissionCapture) { c.observationCanonical = "{}" }, func(c *admissionCapture) { c.configKind = "submitted" }, func(c *admissionCapture) { c.policyVersion++ }} {
		bad := c
		change(&bad)
		if _, err := constructWitness(bad, input, testWitnessLimits()); err == nil {
			t.Fatal("material identity accepted")
		}
	}
}

func TestPreparationWitnessSubmittedOriginAndEmpty(t *testing.T) {
	m := witnessFixture(t)
	m.config.kind = "submitted"
	c, err := constructQualification(m)
	if err != nil {
		t.Fatal(err)
	}
	w, err := constructWitness(c, catalog.DigestBytes([]byte("input")), testWitnessLimits())
	if err != nil {
		t.Fatal(err)
	}
	if decodeWitness(t, w).Decisions[0].PolicyOrigin.Layer != "submitted_exact" {
		t.Fatal("wrong submitted origin")
	}
	// An authoritative empty observation is a legitimate empty result. A1's feed
	// selection prerequisite does not admit a feed from it, so reuse the capture
	// only to test the pure whole-ranking constructor, with its exact claim.
	o := m.catalog.observation
	o.Groups = []catalog.ObservedGroup{}
	canonical, digest, err := o.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	q, err := protocol.ParseStrict(c.qualification.Canonical())
	if err != nil {
		t.Fatal(err)
	}
	q.Object["observation"].Object["observation_digest"] = protocol.Value{Kind: protocol.KindString, String: digest}
	c.qualification, err = protocol.DecodeQualification(protocol.Canonical(q))
	if err != nil {
		t.Fatal(err)
	}
	c.observationCanonical = string(canonical)
	w, err = constructWitness(c, catalog.DigestBytes([]byte("input")), testWitnessLimits())
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range decodeWitness(t, w).Decisions {
		if d.Available || d.Rank != nil || d.Outcome != "unavailable" {
			t.Fatal("unobserved intent authorized")
		}
	}
}

func TestPreparationWitnessUnverifiableAndProviderLists(t *testing.T) {
	for _, change := range []func(*qualificationMaterial){
		func(m *qualificationMaterial) { m.catalog.observation.Completeness.Value.Complete = false },
		func(m *qualificationMaterial) {
			m.catalog.observation.Groups[0].Available.State = "unavailable"
			m.catalog.observation.Groups[0].Available.Value = nil
			reason := "OBSERVATION_EVIDENCE_UNAVAILABLE"
			m.catalog.observation.Groups[0].Available.ReasonCode = &reason
		},
		func(m *qualificationMaterial) {
			m.catalog.observation.Groups = append(m.catalog.observation.Groups, m.catalog.observation.Groups[0])
		},
	} {
		m := witnessFixture(t)
		change(&m)
		if _, err := constructQualification(m); err == nil {
			t.Fatal("unverifiable material accepted")
		}
	}
	m := qualificationFixture(t, "automatic_source")
	p := anime.SubgroupsSection{MaxRss: 3, Entries: []anime.SubgroupScoreEntry{}, ProviderDefaults: &anime.ProviderGroupDefaults{Mikan: &anime.ProviderGroupDefault{Groups: []string{"10", "11"}, Mode: "whitelist", Score: 80}}}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	m.config.business.Sections[anime.SectionSubgroups] = raw
	m.config.digest, err = m.config.business.Digest()
	if err != nil {
		t.Fatal(err)
	}
	c, err := constructQualification(m)
	if err != nil {
		t.Fatal(err)
	}
	w, err := constructWitness(c, catalog.DigestBytes([]byte("input")), testWitnessLimits())
	if err != nil {
		t.Fatal(err)
	}
	v := decodeWitness(t, w)
	if len(v.Decisions) != 2 || v.Decisions[1].GroupRef != "11" || v.Decisions[1].Available || v.Decisions[1].DisplayName.State != "absent" || v.Decisions[1].Rank != nil {
		t.Fatal("provider list union lost or authorized unseen ref")
	}
}
