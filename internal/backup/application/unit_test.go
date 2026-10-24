package application

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testHeader(payload []byte) Header {
	sum := sha256.Sum256(payload)
	return Header{
		App: AppName, Format: FormatVersion, SchemaVersion: "038_drop_backup_runs.sql",
		CreatedAt:    time.Date(2026, 10, 20, 1, 2, 3, 0, time.UTC),
		PayloadBytes: int64(len(payload)), PayloadSHA256: hex.EncodeToString(sum[:]),
	}
}

func TestHeaderRoundTrip(t *testing.T) {
	payload := []byte("PGDMP-payload")
	header := testHeader(payload)
	encoded, err := EncodeHeader(header)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(encoded, []byte(Magic)) {
		t.Fatalf("encoded header lacks the signature: %q", encoded)
	}
	reader := bufio.NewReader(bytes.NewReader(append(encoded, payload...)))
	got, err := ReadHeader(reader)
	if err != nil {
		t.Fatal(err)
	}
	if got != header {
		t.Fatalf("header = %+v, want %+v", got, header)
	}
	rest := new(bytes.Buffer)
	if _, err := rest.ReadFrom(reader); err != nil || rest.String() != string(payload) {
		t.Fatalf("reader is not at the payload: %q, %v", rest.String(), err)
	}
}

func TestReadHeaderRejectsForeignAndMalformedFiles(t *testing.T) {
	valid, err := EncodeHeader(testHeader([]byte("x")))
	if err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSuffix(strings.TrimPrefix(string(valid), Magic), "\n")
	cases := map[string]string{
		"pg_dump archive":  "PGDMP\x01\x0e\x00",
		"empty":            "",
		"no header line":   Magic,
		"unterminated":     Magic + line,
		"not json":         Magic + "hello\n",
		"unknown field":    Magic + strings.Replace(line, `"app"`, `"extra":1,"app"`, 1) + "\n",
		"other app":        Magic + strings.Replace(line, `"bansoko"`, `"other"`, 1) + "\n",
		"other format":     Magic + strings.Replace(line, `"format":1`, `"format":2`, 1) + "\n",
		"bad schema":       Magic + strings.Replace(line, "038_drop_backup_runs.sql", "../038.sql", 1) + "\n",
		"two objects":      Magic + line + line + "\n",
		"header too long":  Magic + strings.Repeat(" ", maxHeaderLineBytes+1) + "\n",
		"negative payload": Magic + strings.Replace(line, `"payload_bytes":1`, `"payload_bytes":-1`, 1) + "\n",
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ReadHeader(bufio.NewReader(strings.NewReader(content))); !errors.Is(err, ErrInvalidFile) {
				t.Fatalf("ReadHeader error = %v, want ErrInvalidFile", err)
			}
		})
	}
}

func TestFileNameCarriesAppTimeAndSchema(t *testing.T) {
	got := FileName(time.Date(2026, 10, 20, 9, 8, 7, 0, time.FixedZone("x", 8*3600)), "038_drop_backup_runs.sql")
	if got != "bansoko-20261020T010807Z-schema038.bansoko-backup" {
		t.Fatalf("FileName = %q", got)
	}
}

func TestSideNamesParseOnlyTheirOwnDatabase(t *testing.T) {
	for _, kind := range []string{kindRestoring, kindRestored, kindReplaced} {
		name := sideName("bansoko", kind, 1760000000)
		gotKind, stamp, ok := parseSideName("bansoko", name)
		if !ok || gotKind != kind || stamp != 1760000000 {
			t.Fatalf("parseSideName(%q) = %q %d %v", name, gotKind, stamp, ok)
		}
		if len(sideName(strings.Repeat("a", 40), kind, 1760000000)) > 63 {
			t.Fatalf("side name of the longest database name exceeds the identifier limit")
		}
	}
	for _, name := range []string{
		"bansoko", "bansoko__restored_", "bansoko__restored_01", "bansoko__restored_1x",
		"bansoko__other_1", "bansoko_x__restored_1", "other__restored_1", "bansoko__restored_-1",
	} {
		if _, _, ok := parseSideName("bansoko", name); ok {
			t.Fatalf("parseSideName(%q) accepted a foreign name", name)
		}
	}
	if validDatabaseName(`bansoko"; DROP`) || validDatabaseName(strings.Repeat("a", 41)) || !validDatabaseName("bansoko_test_disp") {
		t.Fatal("database name validation is wrong")
	}
}

func TestReceivePayloadStoresExactlyTheDeclaredBytes(t *testing.T) {
	payload := []byte("PGDMP-payload")
	header := testHeader(payload)
	dir := t.TempDir()
	if err := receivePayload(bytes.NewReader(payload), filepath.Join(dir, "ok"), header); err != nil {
		t.Fatalf("exact payload refused: %v", err)
	}
	cases := map[string][]byte{
		"short":    payload[:len(payload)-1],
		"trailing": append(append([]byte{}, payload...), '!'),
		"changed":  append([]byte("X"), payload[1:]...),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if err := receivePayload(bytes.NewReader(body), filepath.Join(dir, name), header); !errors.Is(err, ErrInvalidFile) {
				t.Fatalf("receivePayload error = %v, want ErrInvalidFile", err)
			}
		})
	}
}

func TestSchemaNumber(t *testing.T) {
	if got, err := SchemaNumber("038_drop_backup_runs.sql"); err != nil || got != "038" {
		t.Fatalf("SchemaNumber = %q, %v", got, err)
	}
	if _, err := SchemaNumber("baseline.sql"); err == nil {
		t.Fatal("SchemaNumber accepted a version without a number")
	}
}
