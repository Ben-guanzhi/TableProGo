package importfile

// CSV stream reading. DecodingReader delegates to the kernel's textio
// (§352): the BOM/UTF-16/GB18030 decoding rules are maintained in one place
// and shared by every client, while this package keeps the chunked CSV
// reader and its test entry point.

import (
	"encoding/csv"
	"fmt"
	"io"
	"strconv"

	"github.com/Ben-guanzhi/dbcore/textio"
)

// StreamChunkSize is how many rows one streamed chunk carries.
const StreamChunkSize = 1000

// DecodingReader 委托内核 textio（保留本包名，调用点零改动）。
var DecodingReader = textio.DecodingReader

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
