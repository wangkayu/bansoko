package domain

import (
	"sort"
	"strings"

	"bansoko/internal/templatefacts"
)

// varsInputMaxFiles is the torrent manifest's file bound
// (catalog/domain.MaxTorrentManifestFiles).
const varsInputMaxFiles = 20000

// registerVarsFacts registers VarsFactsV1, the closed shape of the
// naming-vars-v1 release fact object (known_inputs.facts): exactly the
// templatefacts Vars input facts, grouped by root, each present and either
// null or a value of its registered type. VarsInputV1 is the input the
// frozen program read: the title, the files and that fact object.
func registerVarsFacts() {
	roots := map[string][]field{}
	for _, fact := range templatefacts.VarsInputFacts() {
		root, leaf, _ := strings.Cut(fact.Path, ".")
		var typ Schema
		switch fact.Type {
		case templatefacts.TypeInteger:
			typ = Nullable(I64)
		case templatefacts.TypeBoolean:
			typ = Nullable(Bool)
		case templatefacts.TypeString:
			typ = Nullable(Text(1, 8192))
		default:
			panic("acquisition: unsupported vars input fact type " + string(fact.Type))
		}
		roots[root] = append(roots[root], F(leaf, typ))
	}
	names := make([]string, 0, len(roots))
	for root := range roots {
		names = append(names, root)
	}
	sort.Strings(names)
	fields := make([]field, 0, len(names))
	for _, root := range names {
		fields = append(fields, F(root, Obj(roots[root]...).Optional(templatefacts.VarsInputOptionalMembers[root]...)))
	}
	register("VarsFactsV1", Obj(fields...))
	// The files are every file of the release, as many as a torrent manifest
	// may list; their bytes are bounded by the program's input bound
	// (namingvars.MaxInputBytes), which acceptance checks before it freezes.
	register("VarsInputV1", Obj(
		F("release_title", FVOf(Display)), F("files", Array(Ref("VarsFileV1"), 0, varsInputMaxFiles)), F("facts", Ref("VarsFactsV1")),
	))
}
