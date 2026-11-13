module navop-containers

go 1.25.0

require (
	github.com/dbcore/dbcore v0.1.0
	golang.org/x/crypto v0.55.0
	tablepro-go v0.0.0
)

require (
	github.com/kr/fs v0.1.0 // indirect
	github.com/pkg/sftp v1.13.12 // indirect
	golang.org/x/net v0.58.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
)

// W-C1: the containers plugin. It DEPENDS on tunnel.proxy (provided by
// navop-ssh) and declares connections.store; the dependency is the plan's
// canonical one-way edge (§6.5: no cycle by construction), so the lifecycle
// tests can drive "Inactive until ssh appears, dead the moment ssh dies".
replace tablepro-go => ../..

replace github.com/dbcore/dbcore => ../../../dbcore
