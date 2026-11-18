package metrics

// HostRaw is one reading of the node's own resources. Counters are cumulative.
type HostRaw struct {
	CPUBusy, CPUTotal uint64 // jiffies
	Cores             int
	MemTotal, MemUsed uint64 // bytes
	Load              [3]float64
	DiskTotal         uint64 // bytes, filesystem holding the data dir
	DiskUsed          uint64
	UptimeSec         float64
	Net               map[string]NetRaw
}

// NetRaw is cumulative traffic on one interface.
type NetRaw struct {
	Rx, Tx uint64 // bytes
}
