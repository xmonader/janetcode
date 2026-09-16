package memory

import (
	"strings"
	"testing"
)

func TestParseDream(t *testing.T) {
	ops := parseDream(`{"action":"drop","name":"old-fact"}
{"action":"merge","name":"build","type":"project","description":"how to build","body":"make test","replaces":["build-a","build-b"]}
not json`)
	if len(ops) != 2 {
		t.Fatalf("%+v", ops)
	}
	if ops[0].Name != "old-fact" || ops[0].Action != "drop" {
		t.Fatalf("drop = %+v", ops[0])
	}
	if ops[1].Name != "build" || strings.Join(ops[1].Replaces, ",") != "build-a,build-b" {
		t.Fatalf("merge = %+v", ops[1])
	}
}
