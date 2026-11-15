module navop-mqtt

go 1.25.0

require github.com/Ben-guanzhi/dbcore v0.2.0

// W-Q1: the MQTT plugin. Publish is an EMISSION (withheld behind the host's Safe
// Mode); subscribe is an ACQUISITION (the subscription is a reversible row in the
// host's mqtt.subscriptions table). One-way dependency on the host's
// connections.store; no cycle (§6.5).
replace tablepro-go => ../..
