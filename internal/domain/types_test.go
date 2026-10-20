package domain

import "testing"

func TestGroupKeyRequiresKnownIdentity(t *testing.T) {
	a := Service{Host: "spark", PID: 42, Identity: "boot:42:start", Port: 3000, Address: "127.0.0.1", Family: "IPv4"}
	b := a
	b.Port, b.Address, b.Family = 4000, "::1", "IPv6"
	if a.GroupKey() != b.GroupKey() {
		t.Fatal("one verified process should group across ports and families")
	}
	b.Identity = "boot:42:reused"
	if a.GroupKey() == b.GroupKey() {
		t.Fatal("reused PIDs must remain distinct")
	}
	a.PID, b.PID, a.Identity, b.Identity = 0, 0, "", ""
	if a.GroupKey() == b.GroupKey() {
		t.Fatal("unknown owners must not be grouped together")
	}
	a.ContainerID, b.ContainerID = "container-one", "container-one"
	if a.GroupKey() != b.GroupKey() {
		t.Fatal("verified container endpoints should group")
	}
	b.Host = "another-spark"
	if a.GroupKey() == b.GroupKey() {
		t.Fatal("containers on different hosts must remain distinct")
	}
}

func TestUnknownDisplayNameExplainsMissingIdentity(t *testing.T) {
	if got := (Service{Name: "unknown"}).DisplayName(); got != "Unidentified service" {
		t.Fatalf("unknown process label: %q", got)
	}
}
