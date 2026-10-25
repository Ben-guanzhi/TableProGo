package importfile

// Streaming decode + chunked CSV reading: the importer's memory stays bounded
// by one chunk instead of the whole file, and common non-UTF-8 encodings are
// handled at the byte level so rows never arrive mojibake.

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"io"
	"strconv"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
)

// StreamChunkSize is how many rows one streamed chunk carries. The client
// never holds more than one chunk plus its open transaction, and the first
// chunk doubles as the CREATE TABLE type-inference sample.
const StreamChunkSize = 1000

// sniffSize bounds the encoding probe. A prefix this large is decisive for
// UTF-8 vs GB18030 in practice and costs one read.
const sniffSize = 64 * 1024

// DecodingReader transparently decodes common non-UTF-8 import files: a BOM
// picks UTF-8 (stripped) or UTF-16; without a BOM the first sniffSize bytes
// are probed, and bytes that are not valid UTF-8 fall back to GB18030 (a
// superset of GBK and GB2312, the usual encoding of Chinese-language exports).
// A BOM-less UTF-16 file is left alone: guessing it from NUL bytes alone has
// too many false positives.
func DecodingReader(r io.Reader) io.Reader {
	head := make([]byte, 3)
	n, _ := io.ReadFull(r, head)
	head = head[:n]
	switch {
	case len(head) >= 3 && head[0] == 0xEF && head[1] == 0xBB && head[2] == 0xBF:
		return r // UTF-8 BOM: stripped, the rest is already UTF-8
	case len(head) >= 2 && head[0] == 0xFF && head[1] == 0xFE:
		// The probe read three bytes but the BOM is two: the third byte is
		// already data and must go back.
		return transform.NewReader(io.MultiReader(bytes.NewReader(head[2:]), r),
			unicode.UTF16(unicode.LittleEndian, unicode.IgnoreBOM).NewDecoder())
	case len(head) >= 2 && head[0] == 0xFE && head[1] == 0xFF:
		return transform.NewReader(io.MultiReader(bytes.NewReader(head[2:]), r),
			unicode.UTF16(unicode.BigEndian, unicode.IgnoreBOM).NewDecoder())
	}

	sniff := make([]byte, sniffSize)
	m, _ := io.ReadFull(r, sniff)
	sniff = sniff[:m]
	combined := append(head, sniff...)
	// A multi-byte rune split by the sniff boundary would fail utf8.Valid on a
	// genuine UTF-8 file; drop up to three trailing bytes before concluding.
	if !utf8.Valid(combined) {
		for k := 1; k <= 3 && k < len(combined); k++ {
			if utf8.Valid(combined[:len(combined)-k]) {
				combined = combined[:len(combined)-k]
				break
			}
		}
	}
	prefix := io.MultiReader(bytes.NewReader(combined), r)
	if utf8.Valid(combined) {
		return prefix
	}
	return transform.NewReader(prefix, simplifiedchinese.GB18030.NewDecoder())
}

// CSVStream reads a delimited file chunk by chunk and hands each chunk to fn.
// cols comes from the header row (or is generated when HasHeader is false);
// isFirst marks the first call, which carries the import's setup. A header-only
// file still calls fn once with no rows so setup can run.
func CSVStream(r io.Reader, opts Options, fn func(cols []string, chunk [][]*string, isFirst bool) error) error {
	cr := csv.NewReader(DecodingReader(r))
	cr.FieldsPerRecord = -1
	// Normalize() fills the delimiter in; default here too so the package API
	// cannot panic on a zero Options (a missing rune would index []rune("")).
	delimiter := opts.Delimiter
	if delimiter == "" {
		delimiter = ","
	}
	cr.Comma = []rune(delimiter)[0]

	var cols []string
	var chunk [][]*string
	first := true
	call := func() error {
		if !first && len(chunk) == 0 {
			return nil
		}
		if err := fn(cols, chunk, first); err != nil {
			return err
		}
		first = false
		chunk = chunk[:0]
		return nil
	}
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("import: parse csv: %w", err)
		}
		if cols == nil {
			if opts.HasHeader {
				cols = rec
				continue
			}
			cols = make([]string, len(rec))
			for i := range cols {
				cols[i] = "column" + strconv.Itoa(i+1)
			}
		}
		row := make([]*string, len(cols))
		for i := range cols {
			if i < len(rec) {
				cell := rec[i]
				row[i] = &cell
			}
		}
		chunk = append(chunk, row)
		if len(chunk) >= StreamChunkSize {
			if err := call(); err != nil {
				return err
			}
		}
	}
	if cols == nil {
		return fmt.Errorf("import: file is empty")
	}
	return call()
}
