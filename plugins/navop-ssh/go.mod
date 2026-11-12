module navop-ssh

go 1.25.0

require (
	github.com/dbcore/dbcore v0.1.0
	golang.org/x/crypto v0.55.0
	tablepro-go v0.0.0
)

require (
	golang.org/x/net v0.58.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
)

// W-T1: the navop-ssh workbench plugin builds against the host's shared SSH
// library (tablepro-go/sshx — the dial chain, auth and OpenSSH-config parsing
// the host's DB tunnels use too) and the kernel's workbench protocol. One copy
// of the SSH capability, not two forks.
replace tablepro-go => ../..

replace github.com/dbcore/dbcore => ../../../dbcore
