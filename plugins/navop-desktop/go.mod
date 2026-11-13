module navop-desktop

go 1.25.0

require github.com/dbcore/dbcore v0.1.0

// W-D1: the remote-desktop plugin. RDP is an EMISSION (mstsc is spawned; the only
// compensatable part is the .rdp file the HOST writes and whose deletion the host
// tracks as the effect inverse). VNC is an ACQUISITION: frames ride the channel.
replace tablepro-go => ../..

replace github.com/dbcore/dbcore => ../../../dbcore
