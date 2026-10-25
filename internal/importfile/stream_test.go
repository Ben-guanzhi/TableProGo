package importfile

import (
	"bytes"
	"encoding/csv"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/unicode"
)

// A GB18030 (GBK family) file is the common case in Chinese-language exports;
// it must decode instead of arriving as mojibake.
func TestDecodingReaderGB18030(t *testing.T) {
	utf8CSV := "name,city\n张三,北京\n"
	gb, err := simplifiedchinese.GB18030.NewEncoder().Bytes([]byte(utf8CSV))
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	cols, rows, err := ParseCSV(DecodingReader(bytes.NewReader(gb)), Options{HasHeader: true, Delimiter: ","})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cols[0] != "name" || len(rows) != 1 || *rows[0][0] != "张三" || *rows[0][1] != "北京" {
		t.Fatalf("decoded = %v, %v", cols, rows)
	}
}

// UTF-16 with a BOM (what some Windows tools write) decodes too.
func TestDecodingReaderUTF16BOM(t *testing.T) {
	utf8CSV := "id,label\n1,ok\n"
	enc := unicode.UTF16(unicode.LittleEndian, unicode.UseBOM).NewEncoder()
	utf16, err := enc.Bytes([]byte(utf8CSV))
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	cols, rows, err := ParseCSV(DecodingReader(bytes.NewReader(utf16)), Options{HasHeader: true, Delimiter: ","})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cols[0] != "id" || len(rows) != 1 || *rows[0][1] != "ok" {
		t.Fatalf("decoded = %v, %v", cols, rows)
	}
}

// CSVStream hands out chunks of StreamChunkSize rows, keeps the header out of
// the data, and still calls the sink for a header-only file (setup must run).
func TestCSVStreamChunking(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("id,name\n")
	for i := 1; i <= StreamChunkSize+7; i++ {
		sb.WriteString(strconv.Itoa(i) + ",n\n")
	}

	calls := 0
	var firstCols []string
	totalRows := 0
	err := CSVStream(strings.NewReader(sb.String()), Options{HasHeader: true, Delimiter: ","},
		func(cols []string, chunk [][]*string, isFirst bool) error {
			calls++
			if isFirst {
				firstCols = cols
			}
			totalRows += len(chunk)
			return nil
		})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if calls != 2 { // one full chunk of 1000, then the 7-row tail at EOF
		t.Fatalf("calls = %d", calls)
	}
	if firstCols == nil || firstCols[0] != "id" {
		t.Fatalf("cols = %v", firstCols)
	}
	if totalRows != StreamChunkSize+7 {
		t.Fatalf("rows = %d, want %d", totalRows, StreamChunkSize+7)
	}
}

// A header-only file calls the sink once with zero rows, so the import's
// create-table path still runs.
func TestCSVStreamHeaderOnly(t *testing.T) {
	calls := 0
	err := CSVStream(strings.NewReader("a,b\n"), Options{HasHeader: true, Delimiter: ","},
		func(cols []string, chunk [][]*string, isFirst bool) error {
			calls++
			if !isFirst || len(chunk) != 0 || cols[1] != "b" {
				t.Fatalf("unexpected call: cols=%v chunk=%d first=%v", cols, len(chunk), isFirst)
			}
			return nil
		})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
}

// A generated-column file (no header) names columns from the first record.
func TestCSVStreamNoHeader(t *testing.T) {
	cols, rows, err := ParseCSV(strings.NewReader("1,x\n2,y\n"), Options{HasHeader: false, Delimiter: ","})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cols[0] != "column1" || len(rows) != 2 {
		t.Fatalf("cols=%v rows=%d", cols, len(rows))
	}
	// The csv reader itself must have been exercised via the decoding wrapper:
	// a GB byte sequence in the data decodes on the same path.
	gb, _ := simplifiedchinese.GB18030.NewEncoder().Bytes([]byte("3,北京\n"))
	cr := csv.NewReader(DecodingReader(bytes.NewReader(gb)))
	rec, err := cr.Read()
	if err != nil || rec[1] != "北京" {
		t.Fatalf("decoded record = %v, %v", rec, err)
	}
}
