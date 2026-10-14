# TablePro Go

A fast, lightweight database client built with Wails3 + Go, inspired by TablePro.

## Features

- **Multiple Database Support**: MySQL, PostgreSQL, SQLite
- **Modern UI**: React + TypeScript + Tailwind CSS
- **Query Editor**: Syntax highlighting, auto-complete, execute with ⌘+Enter
- **Data Grid**: Virtual scrolling, column sorting, copy/paste
- **Connection Management**: Save, edit, test connections
- **Schema Browser**: Browse databases, tables, columns
- **Import/Export**: CSV, JSON, SQL, Excel formats

## Tech Stack

- **Backend**: Go 1.25 with Wails3
- **Frontend**: React 18 + TypeScript + Vite
- **Database Drivers**:
  - MySQL/MariaDB: `github.com/go-sql-driver/mysql`
  - PostgreSQL: `github.com/lib/pq`
  - SQLite: `github.com/mattn/go-sqlite3`

## Prerequisites

- Go 1.25
- Node.js 18+
- Wails3 CLI: `go install github.com/wailsapp/wails/v3/cmd/wails@latest`

## Development

### Install Dependencies

```bash
# Install Go dependencies
go mod tidy

# Install frontend dependencies
cd frontend && npm install
```

### Run in Development Mode

```bash
wails dev
```

### Build for Production

```bash
wails build
```

## Project Structure

```
TableProGo/
├── main.go              # Entry point: HTTP JSON API server, Wails3 service, catalog SQL per dialect
├── changes.go           # Grid edits → UPDATE/INSERT/DELETE, applied in one transaction
├── filters.go           # Column filters → WHERE clauses
├── fkvalues.go          # Foreign-key dropdown values
├── search.go            # Cross-database object search
├── exportsql.go         # Export result set as INSERT script
├── exportxlsx.go        # XLSX API wrapper: base64 for the JSON API + refusals
├── tableactions.go      # DROP / TRUNCATE / RENAME
├── secrets_windows.go   # DPAPI password encryption
├── secrets_other.go     # Plain-text fallback on non-Windows
├── go.mod / go.sum
├── tableprosql/         # shared module: per-dialect SQL text + ColumnType predicates
├── tableproxlsx/        # shared module: xlsx rendering (BuildWorkbook)
└── frontend/            # React 18 + TypeScript + Tailwind (runs in the WebView)
    ├── src/
    │   ├── App.tsx
    │   ├── api.ts       # Talks to the local HTTP API (injected port)
    │   └── components/  # Sidebar, DataGrid, QueryEditor, dialogs
    └── tests/           # node:test suites (exports, a11y, DDL templates, …)
```

## Usage

1. **Create a Connection**: Click the "+" button in the sidebar
2. **Connect**: Click on a saved connection to connect
3. **Execute Query**: Write SQL in the editor and press ⌘+Enter
4. **Browse Schema**: Expand databases and tables in the sidebar
5. **Export Results**: Use the export button in the toolbar

## Data Storage

Saved connections, settings, and query history live in the user config
directory (`%AppData%\TableProGo` on Windows) — never next to the executable.
Passwords are encrypted with Windows DPAPI on Windows, and stored in the
system keyring (macOS Keychain, Linux Secret Service) elsewhere; only a machine
with no usable keyring at all falls back to the legacy plain-text file, which
the connection dialog then warns about. Files left beside the
executable by older builds are migrated there automatically on first launch.

## Data Grid Paging

Stepping through pages reads the next page by **seeking from the visible
page's primary key** instead of counting rows (`OFFSET`), provided the table
lives in MySQL, PostgreSQL or SQLite and its key columns are integers or text —
the only kinds whose values come back out of the grid exactly as the engine
compares them. (The grid only offers 上一页/下一页 navigation, so the page on
screen always carries the cursor to step from.) Keys of other types —
decimal/numeric, float, date or time, binary, UUID — are **deliberately
refused**: what the grid shows for them is not the value the engine would
compare, and seeking by the shown text could skip or repeat a row without
any error to show for it. Such tables, and every table in SQL Server or Oracle
(whose paging is a server-side cursor statement of its own), keep the old
`OFFSET` paging, which is always correct.

## Supported Databases

| Database   | Version | Status         |
|------------|---------|----------------|
| MySQL      | 5.7+    | ✅ Supported   |
| MariaDB    | 10.2+   | ✅ Supported   |
| PostgreSQL | 12+     | ✅ Supported   |
| SQLite     | 3.x     | ✅ Supported   |
| SQL Server | 2012+   | ✅ Supported   |
| Oracle     | 12c+    | ✅ Supported   |

## Import/Export Formats

| Format       | Export |
|--------------|--------|
| CSV          | ✅     |
| JSON         | ✅     |
| SQL (INSERT) | ✅     |
| Excel (XLSX) | ✅     |

## License

MIT License

## Acknowledgments

- [TablePro](https://github.com/nickvdyck/tablepro) - Original TablePro macOS app
- [Wails](https://wails.io/) - Go framework for desktop apps
- [React](https://react.dev/) - Frontend library
- [Tailwind CSS](https://tailwindcss.com/) - Utility-first CSS framework
