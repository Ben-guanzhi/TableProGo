module tablepro-go

go 1.25.0

require (
	github.com/denisenkom/go-mssqldb v0.12.3
	github.com/go-sql-driver/mysql v1.8.1
	github.com/lib/pq v1.10.9
	github.com/mattn/go-sqlite3 v1.14.22
	github.com/sijms/go-ora/v2 v2.9.0
	github.com/wailsapp/wails/v3 v3.0.0-beta.24
	github.com/xuri/excelize/v2 v2.11.0 // indirect
	github.com/zalando/go-keyring v0.2.8
)

require (
	filippo.io/edwards25519 v1.1.0 // indirect
	github.com/adrg/xdg v0.5.3 // indirect
	github.com/coder/websocket v1.8.14 // indirect
	github.com/danieljoos/wincred v1.2.3 // indirect
	github.com/go-ole/go-ole v1.3.0 // indirect
	github.com/godbus/dbus/v5 v5.2.2 // indirect
	github.com/golang-sql/civil v0.0.0-20190719163853-cb61b32ac6fe // indirect
	github.com/golang-sql/sqlexp v0.1.0 // indirect
	github.com/mattn/go-colorable v0.1.14 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/richardlehane/mscfb v1.0.7 // indirect
	github.com/richardlehane/msoleps v1.0.6 // indirect
	github.com/tiendc/go-deepcopy v1.7.2 // indirect
	github.com/xuri/efp v0.0.1 // indirect
	github.com/xuri/nfp v0.0.2-0.20250530014748-2ddeb826f9a9 // indirect
	golang.org/x/crypto v0.53.0 // indirect
	golang.org/x/net v0.56.0 // indirect
	golang.org/x/sys v0.46.0 // indirect
	golang.org/x/text v0.39.0 // indirect
)

// These two modules live inside this repo, so they are pinned to the working
// tree rather than to a tag: editing tableprosql/ and rebuilding must show the
// edit, with no step in between. fynequery has no such local copy and instead
// resolves the same two from the tableprosql/v* / tableproxlsx/v* tags, which
// is what makes it buildable from its own clone.
require github.com/Ben-guanzhi/TableProGo/tableprosql v0.1.0

require github.com/Ben-guanzhi/TableProGo/tableproxlsx v0.1.0

replace github.com/Ben-guanzhi/TableProGo/tableproxlsx => ./tableproxlsx

replace github.com/Ben-guanzhi/TableProGo/tableprosql => ./tableprosql
