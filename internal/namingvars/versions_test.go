package namingvars

import (
	"context"
	"errors"
	"strings"
	"testing"
)

const secondSource = "vars.series = {name: \"second\"};\n"

func seeded(t *testing.T) (*fakePersistence, Store) {
	t.Helper()
	persistence := newFakePersistence()
	store := NewStore(persistence)
	fp, err := SourceFingerprint(validVarsSource)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := persistence.Seed(context.Background(), SaveParams{Source: validVarsSource, Fingerprint: fp, Origin: OriginFile}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Current(context.Background()); err != nil {
		t.Fatal(err)
	}
	return persistence, store
}

func TestSaveVersionActivatesWithoutRestart(t *testing.T) {
	persistence, store := seeded(t)
	info, err := store.SaveVersion(context.Background(), secondSource, "  second  ", OriginEditor, 1)
	if err != nil {
		t.Fatal(err)
	}
	if info.Revision != 2 || !info.Active || info.Note != "second" || info.Origin != OriginEditor {
		t.Fatalf("saved version = %+v", info)
	}
	vars, fp, err := Current()
	if err != nil || fp != info.Digest || vars["series"]["name"] != "second" {
		t.Fatalf("process active = %v / %q / %v, want the saved program at once", vars, fp, err)
	}
	if got := persistence.activeRevision(); got != 2 {
		t.Fatalf("active revision = %d", got)
	}
	// Saving the active source again is not a new version.
	again, err := store.SaveVersion(context.Background(), secondSource, "", OriginEditor, 2)
	if err != nil || again.Revision != 2 || len(persistence.versions) != 2 {
		t.Fatalf("resave = %+v / %v / %d versions", again, err, len(persistence.versions))
	}
}

func TestSaveVersionConflictAndInvalidChangeNothing(t *testing.T) {
	persistence, store := seeded(t)
	before, _, _ := Current()

	_, err := store.SaveVersion(context.Background(), secondSource, "", OriginEditor, 7)
	var conflict *ConflictError
	if !errors.As(err, &conflict) || conflict.Current.Revision != 1 {
		t.Fatalf("stale save = %v, want a conflict naming revision 1", err)
	}
	for name, source := range map[string]string{
		"syntax":    "vars.a = {b: };",
		"dynamic":   "vars.a = {b: eval('1')};",
		"expansion": "vars.a = {b: [1].map(x => x)};",
		"empty":     "",
		"runtime":   "vars.a = {b: input.nothing.at.all};",
		"missing":   "vars.a = {b: input.anime.nothing};",
	} {
		_, err := store.SaveVersion(context.Background(), source, "", OriginEditor, 1)
		var invalid *InvalidSourceError
		if !errors.As(err, &invalid) || len(invalid.Diagnostics) == 0 {
			t.Fatalf("%s: save = %v, want an invalid-source error", name, err)
		}
	}
	_, err = store.SaveVersion(context.Background(), secondSource, strings.Repeat("n", MaxNoteLength+1), OriginEditor, 1)
	if err == nil {
		t.Fatal("an oversized note must be refused")
	}
	if len(persistence.versions) != 1 || persistence.activeRevision() != 1 {
		t.Fatalf("rejected saves left %d versions, active %d", len(persistence.versions), persistence.activeRevision())
	}
	after, fp, _ := Current()
	if fp != persistence.versions[0].Digest || after["anime"]["ep"] != before["anime"]["ep"] {
		t.Fatal("a rejected save changed the process active program")
	}
}

func TestActivateRevisionRollsBack(t *testing.T) {
	persistence, store := seeded(t)
	if _, err := store.SaveVersion(context.Background(), secondSource, "", OriginEditor, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ActivateRevision(context.Background(), 1, 1); err == nil {
		t.Fatal("activation from a stale expected revision must conflict")
	}
	info, err := store.ActivateRevision(context.Background(), 2, 1)
	if err != nil || info.Revision != 1 || !info.Active {
		t.Fatalf("rollback = %+v / %v", info, err)
	}
	vars, fp, _ := Current()
	if fp != persistence.versions[0].Digest || vars["anime"]["ep"] != "01" {
		t.Fatalf("process active after rollback = %v / %q", vars, fp)
	}
	if _, err := store.ActivateRevision(context.Background(), 1, 99); !errors.Is(err, ErrVersionNotFound) {
		t.Fatalf("unknown revision = %v", err)
	}
	if len(persistence.versions) != 2 {
		t.Fatalf("a rollback must not create versions: %d", len(persistence.versions))
	}
}

func TestDesignInputLetsAProgramReadReleaseFacts(t *testing.T) {
	source := "vars.show = {title: input.anime.title ?? \"\", group: input.rt.subgroup ?? \"\", ep: input.ep.number ?? \"\"};"
	vars, diagnostics := Validate(source)
	if len(diagnostics) != 0 {
		t.Fatalf("a program reading release facts must validate: %+v", diagnostics)
	}
	if vars["show"]["group"] == "" || vars["show"]["ep"] != "1" {
		t.Fatalf("design output = %v, want the sample release facts", vars)
	}
}
