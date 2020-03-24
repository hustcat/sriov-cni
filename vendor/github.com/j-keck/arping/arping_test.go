package arping

import (
	"testing"
	"net"
	"strings"
)


func TestPingWithInvalidIP(t *testing.T) {
	ip := net.ParseIP("invalid")

	_, _, err := Ping(ip)
	if err == nil {
		t.Error("error expected")
	}

	validateInvalidV4AddrErr(t, err)
}

func TestPingWithV6IP(t *testing.T) {
	ip := net.ParseIP("fe80::e2cb:4eff:fed5:ca4e")

	_, _, err := Ping(ip)
	if err == nil {
		t.Error("error expected")
	}

	validateInvalidV4AddrErr(t, err)
}

func TestGratuitousArpWithInvalidIP(t *testing.T) {
	ip := net.ParseIP("invalid")

	err := GratuitousArp(ip)
	if err == nil {
		t.Error("error expected")
	}

	validateInvalidV4AddrErr(t, err)
}

func TestGratuitousArpWithV6IP(t *testing.T) {
	ip := net.ParseIP("fe80::e2cb:4eff:fed5:ca4e")

	err := GratuitousArp(ip)
	if err == nil {
		t.Error("error expected")
	}

	validateInvalidV4AddrErr(t, err)
}

func validateInvalidV4AddrErr(t *testing.T, err error) {
	if ! strings.Contains(err.Error(), "not a valid v4 Address") {
		t.Errorf("unexpected error: %s", err)
	}
}
