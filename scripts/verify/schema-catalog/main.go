// Command schema-catalog prints a normalized, line-oriented snapshot of a
// PostgreSQL database's public schema: roles, relations, columns, defaults,
// constraints, indexes, triggers, functions (normalized bodies), sequences,
// types, privileges, comments and every table row. Two snapshots diff with
// plain `diff -u`, which is how a rewritten migration baseline is compared
// with the schema the previous migration chain built.
//
//	go run ./scripts/schema-catalog -dsn "$DATABASE_URL" > catalog.txt
//
// It only reads the catalog. -exclude-role drops the connecting owner role
// from ACL and role lines so snapshots taken under different owners compare.
package main

import (
	"database/sql"
	"flag"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func main() {
	dsn := flag.String("dsn", "", "PostgreSQL connection string")
	excludeRole := flag.String("exclude-role", "", "owner role name replaced by OWNER in privileges")
	flag.Parse()
	if *dsn == "" {
		fmt.Fprintln(os.Stderr, "schema-catalog: -dsn is required")
		os.Exit(2)
	}
	db, err := sql.Open("pgx", *dsn)
	if err != nil {
		fail(err)
	}
	defer db.Close()
	out := &snapshot{owner: *excludeRole}
	for _, section := range []func(*sql.DB, *snapshot) error{
		roles, extensions, types, relations, columns, constraints, indexes,
		triggers, functions, sequences, privileges, comments, rows,
	} {
		if err := section(db, out); err != nil {
			fail(err)
		}
	}
	sort.Strings(out.lines)
	for _, line := range out.lines {
		fmt.Println(line)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "schema-catalog:", err)
	os.Exit(1)
}

type snapshot struct {
	owner string
	lines []string
}

func (s *snapshot) add(format string, args ...any) {
	line := fmt.Sprintf(format, args...)
	if s.owner != "" {
		line = regexp.MustCompile(`\b`+regexp.QuoteMeta(s.owner)+`\b`).ReplaceAllString(line, "OWNER")
	}
	s.lines = append(s.lines, line)
}

func query(db *sql.DB, out *snapshot, text string, emit func(cols []string)) error {
	rows, err := db.Query(text)
	if err != nil {
		return fmt.Errorf("%w\n%s", err, text)
	}
	defer rows.Close()
	names, err := rows.Columns()
	if err != nil {
		return err
	}
	for rows.Next() {
		values := make([]sql.NullString, len(names))
		ptrs := make([]any, len(names))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return err
		}
		cols := make([]string, len(values))
		for i, v := range values {
			if v.Valid {
				cols[i] = v.String
			} else {
				cols[i] = "<null>"
			}
		}
		emit(cols)
	}
	return rows.Err()
}

func roles(db *sql.DB, out *snapshot) error {
	return query(db, out, `
SELECT r.rolname, r.rolcanlogin::text, r.rolinherit::text, r.rolcreaterole::text,
       r.rolcreatedb::text, r.rolsuper::text, r.rolreplication::text, r.rolbypassrls::text
FROM pg_roles r
WHERE r.rolname !~ '^pg_' AND r.rolname <> current_user`, func(c []string) {
		out.add("role %s login=%s inherit=%s createrole=%s createdb=%s super=%s repl=%s bypassrls=%s", c[0], c[1], c[2], c[3], c[4], c[5], c[6], c[7])
	})
}

func extensions(db *sql.DB, out *snapshot) error {
	return query(db, out, `SELECT extname FROM pg_extension WHERE extname <> 'plpgsql'`, func(c []string) {
		out.add("extension %s", c[0])
	})
}

func types(db *sql.DB, out *snapshot) error {
	return query(db, out, `
SELECT t.typname, t.typtype::text,
       COALESCE((SELECT string_agg(e.enumlabel, ',' ORDER BY e.enumsortorder) FROM pg_enum e WHERE e.enumtypid = t.oid), ''),
       COALESCE(format_type(t.typbasetype, t.typtypmod), '')
FROM pg_type t JOIN pg_namespace n ON n.oid = t.typnamespace
WHERE n.nspname = 'public' AND t.typtype IN ('e','d','c')
  AND NOT EXISTS (SELECT 1 FROM pg_class c WHERE c.reltype = t.oid AND c.relkind <> 'c')`, func(c []string) {
		out.add("type %s kind=%s enum=%s base=%s", c[0], c[1], c[2], c[3])
	})
}

func relations(db *sql.DB, out *snapshot) error {
	return query(db, out, `
SELECT c.relname, c.relkind::text, c.relpersistence::text,
       COALESCE(pg_get_viewdef(c.oid, true), '')
FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = 'public' AND c.relkind IN ('r','p','v','m','f','S')`, func(c []string) {
		out.add("relation %s kind=%s persistence=%s %s", c[0], c[1], c[2], squash(c[3]))
	})
}

func columns(db *sql.DB, out *snapshot) error {
	return query(db, out, `
SELECT c.relname,
       lpad(row_number() OVER (PARTITION BY c.oid ORDER BY a.attnum)::text, 3, '0'),
       a.attname, format_type(a.atttypid, a.atttypmod), a.attnotnull::text,
       COALESCE(pg_get_expr(d.adbin, d.adrelid), ''), a.attidentity::text, a.attgenerated::text,
       COALESCE(co.collname, '')
FROM pg_attribute a
JOIN pg_class c ON c.oid = a.attrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
LEFT JOIN pg_collation co ON co.oid = a.attcollation AND co.collname <> 'default'
WHERE n.nspname = 'public' AND c.relkind IN ('r','p','v','m','f')
  AND a.attnum > 0 AND NOT a.attisdropped`, func(c []string) {
		out.add("column %s #%s %s %s notnull=%s default=%s identity=%s generated=%s collation=%s", c[0], c[1], c[2], c[3], c[4], squash(c[5]), c[6], c[7], c[8])
	})
}

func constraints(db *sql.DB, out *snapshot) error {
	return query(db, out, `
SELECT c.relname, k.conname, k.contype::text, pg_get_constraintdef(k.oid, true),
       k.condeferrable::text, k.condeferred::text, k.convalidated::text
FROM pg_constraint k
JOIN pg_class c ON c.oid = k.conrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = 'public' AND k.contype <> 'n'`, func(c []string) {
		out.add("constraint %s %s type=%s deferrable=%s deferred=%s validated=%s %s", c[0], c[1], c[2], c[4], c[5], c[6], squash(c[3]))
	})
}

func indexes(db *sql.DB, out *snapshot) error {
	return query(db, out, `
SELECT t.relname, i.relname, pg_get_indexdef(i.oid)
FROM pg_index x
JOIN pg_class i ON i.oid = x.indexrelid
JOIN pg_class t ON t.oid = x.indrelid
JOIN pg_namespace n ON n.oid = t.relnamespace
WHERE n.nspname = 'public'`, func(c []string) {
		out.add("index %s %s %s", c[0], c[1], c[2])
	})
}

func triggers(db *sql.DB, out *snapshot) error {
	return query(db, out, `
SELECT c.relname, t.tgname, pg_get_triggerdef(t.oid, true), t.tgenabled::text
FROM pg_trigger t
JOIN pg_class c ON c.oid = t.tgrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = 'public' AND NOT t.tgisinternal`, func(c []string) {
		out.add("trigger %s %s enabled=%s %s", c[0], c[1], c[3], c[2])
	})
}

func functions(db *sql.DB, out *snapshot) error {
	return query(db, out, `
SELECT p.proname, pg_get_function_identity_arguments(p.oid), pg_get_function_result(p.oid),
       l.lanname, p.provolatile::text, p.prosecdef::text, p.proleakproof::text, p.proisstrict::text,
       COALESCE(array_to_string(p.proconfig, ','), ''), p.prokind::text, p.prosrc
FROM pg_proc p
JOIN pg_namespace n ON n.oid = p.pronamespace
JOIN pg_language l ON l.oid = p.prolang
WHERE n.nspname = 'public'`, func(c []string) {
		out.add("function %s(%s) returns=%s lang=%s volatile=%s secdef=%s leakproof=%s strict=%s kind=%s config=%s body=%s",
			c[0], c[1], c[2], c[3], c[4], c[5], c[6], c[7], c[9], c[8], normalizeBody(c[10]))
	})
}

func sequences(db *sql.DB, out *snapshot) error {
	return query(db, out, `
SELECT c.relname, format_type(s.seqtypid, NULL), s.seqstart::text, s.seqincrement::text,
       s.seqmin::text, s.seqmax::text, s.seqcycle::text,
       COALESCE((SELECT t.relname || '.' || a.attname FROM pg_depend d
                 JOIN pg_class t ON t.oid = d.refobjid
                 JOIN pg_attribute a ON a.attrelid = d.refobjid AND a.attnum = d.refobjsubid
                 WHERE d.objid = c.oid AND d.classid = 'pg_class'::regclass AND d.deptype IN ('a','i') LIMIT 1), '')
FROM pg_sequence s JOIN pg_class c ON c.oid = s.seqrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = 'public'`, func(c []string) {
		out.add("sequence %s type=%s start=%s increment=%s min=%s max=%s cycle=%s owned=%s", c[0], c[1], c[2], c[3], c[4], c[5], c[6], c[7])
	})
}

func privileges(db *sql.DB, out *snapshot) error {
	if err := query(db, out, `
SELECT c.relname, COALESCE(array_to_string(ARRAY(SELECT x::text FROM unnest(c.relacl) x ORDER BY 1), ' '), '<default>')
FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = 'public' AND c.relkind IN ('r','p','v','m','f','S')`, func(c []string) {
		out.add("acl relation %s %s", c[0], c[1])
	}); err != nil {
		return err
	}
	if err := query(db, out, `
SELECT a.attrelid::regclass::text, a.attname, array_to_string(ARRAY(SELECT x::text FROM unnest(a.attacl) x ORDER BY 1), ' ')
FROM pg_attribute a JOIN pg_class c ON c.oid = a.attrelid JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = 'public' AND a.attacl IS NOT NULL`, func(c []string) {
		out.add("acl column %s.%s %s", c[0], c[1], c[2])
	}); err != nil {
		return err
	}
	if err := query(db, out, `
SELECT p.proname || '(' || pg_get_function_identity_arguments(p.oid) || ')',
       COALESCE(array_to_string(ARRAY(SELECT x::text FROM unnest(p.proacl) x ORDER BY 1), ' '), '<default>')
FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
WHERE n.nspname = 'public'`, func(c []string) {
		out.add("acl function %s %s", c[0], c[1])
	}); err != nil {
		return err
	}
	if err := query(db, out, `
SELECT nspname, COALESCE(array_to_string(ARRAY(SELECT x::text FROM unnest(nspacl) x ORDER BY 1), ' '), '<default>')
FROM pg_namespace WHERE nspname = 'public'`, func(c []string) {
		out.add("acl schema %s %s", c[0], c[1])
	}); err != nil {
		return err
	}
	return query(db, out, `
SELECT pg_get_userbyid(d.defaclrole), COALESCE(n.nspname, ''), d.defaclobjtype::text,
       array_to_string(ARRAY(SELECT x::text FROM unnest(d.defaclacl) x ORDER BY 1), ' ')
FROM pg_default_acl d LEFT JOIN pg_namespace n ON n.oid = d.defaclnamespace`, func(c []string) {
		out.add("acl default role=%s schema=%s type=%s %s", c[0], c[1], c[2], c[3])
	})
}

func comments(db *sql.DB, out *snapshot) error {
	if err := query(db, out, `
SELECT c.relname, COALESCE(a.attname, ''), d.description
FROM pg_description d
JOIN pg_class c ON c.oid = d.objoid AND d.classoid = 'pg_class'::regclass
JOIN pg_namespace n ON n.oid = c.relnamespace
LEFT JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum = d.objsubid AND d.objsubid > 0
WHERE n.nspname = 'public'`, func(c []string) {
		out.add("comment relation %s %s %s", c[0], c[1], squash(c[2]))
	}); err != nil {
		return err
	}
	return query(db, out, `
SELECT p.proname || '(' || pg_get_function_identity_arguments(p.oid) || ')', d.description
FROM pg_description d
JOIN pg_proc p ON p.oid = d.objoid AND d.classoid = 'pg_proc'::regclass
JOIN pg_namespace n ON n.oid = p.pronamespace
WHERE n.nspname = 'public'`, func(c []string) {
		out.add("comment function %s %s", c[0], squash(c[1]))
	})
}

// rows prints every row of every table. A freshly built schema only holds the
// rows the migrations seed, so this compares seeds; volatile columns
// (timestamps, generated ids) are blanked.
func rows(db *sql.DB, out *snapshot) error {
	var tables []string
	if err := query(db, out, `
SELECT c.relname FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = 'public' AND c.relkind IN ('r','p')`, func(c []string) {
		tables = append(tables, c[0])
	}); err != nil {
		return err
	}
	volatile := regexp.MustCompile(`"(installed_at|applied_at|created_at|updated_at|checksum)":"[^"]*"`)
	for _, table := range tables {
		if err := query(db, out, fmt.Sprintf(`SELECT row_to_json(t)::text FROM public.%q t`, table), func(c []string) {
			out.add("row %s %s", table, volatile.ReplaceAllString(c[0], `"$1":"*"`))
		}); err != nil {
			return err
		}
	}
	return nil
}

var (
	lineComment  = regexp.MustCompile(`(?m)--[^\n]*$`)
	blockComment = regexp.MustCompile(`(?s)/\*.*?\*/`)
	space        = regexp.MustCompile(`\s+`)
)

// normalizeBody drops SQL comments and collapses whitespace so a function
// body rewritten only in its comments or layout compares equal.
func normalizeBody(body string) string {
	body = blockComment.ReplaceAllString(body, " ")
	body = lineComment.ReplaceAllString(body, " ")
	return strings.TrimSpace(space.ReplaceAllString(body, " "))
}

func squash(text string) string {
	return strings.TrimSpace(space.ReplaceAllString(text, " "))
}
