package xraystatus_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/yet-an-other/xform/internal/xraystatus"
)

// fakeConn is a scripted xraystatus.UnitConn.
type fakeConn struct {
	unitProps map[string]any
	svcProps  map[string]any
	propsErr  error
	closed    bool
}

func (f *fakeConn) GetUnitPropertiesContext(context.Context, string) (map[string]any, error) {
	return f.unitProps, f.propsErr
}

func (f *fakeConn) GetUnitTypePropertiesContext(context.Context, string, string) (map[string]any, error) {
	return f.svcProps, nil
}

func (f *fakeConn) Close() { f.closed = true }

// dialTo builds a dial func returning the scripted connection or error.
func dialTo(conn xraystatus.UnitConn, err error) func(context.Context) (xraystatus.UnitConn, error) {
	return func(context.Context) (xraystatus.UnitConn, error) { return conn, err }
}

func TestSystemBusIsPreferredOverPrivateSocket(t *testing.T) {
	system := &fakeConn{unitProps: map[string]any{"ActiveState": "active", "SubState": "running"}}
	unit := xraystatus.SystemdUnit{
		DialSystem: dialTo(system, nil),
		DialPrivate: func(context.Context) (xraystatus.UnitConn, error) {
			t.Fatal("private socket dialed even though the system bus connected")
			return nil, nil
		},
	}

	info, err := unit.QueryUnit(context.Background(), "xray.service")
	if err != nil {
		t.Fatalf("query unit: %v", err)
	}
	if info.ActiveState != "active" {
		t.Errorf("ActiveState = %q, want active from the system-bus connection", info.ActiveState)
	}
	if !system.closed {
		t.Error("connection not closed after the query")
	}
}

func TestPrivateSocketFallbackOnSystemBusDialError(t *testing.T) {
	private := &fakeConn{unitProps: map[string]any{"ActiveState": "inactive", "SubState": "dead"}}
	privateDialed := false
	unit := xraystatus.SystemdUnit{
		DialSystem: dialTo(nil, errors.New("system bus EPERM")),
		DialPrivate: func(ctx context.Context) (xraystatus.UnitConn, error) {
			privateDialed = true
			return private, nil
		},
	}

	info, err := unit.QueryUnit(context.Background(), "xray.service")
	if err != nil {
		t.Fatalf("query unit: %v", err)
	}
	if !privateDialed {
		t.Error("private socket never dialed after the system bus dial failed")
	}
	if info.ActiveState != "inactive" {
		t.Errorf("ActiveState = %q, want inactive from the private-socket connection", info.ActiveState)
	}
	if !private.closed {
		t.Error("connection not closed after the query")
	}
}

func TestDoubleDialFailureNamesBothCauses(t *testing.T) {
	systemErr := errors.New("system bus EPERM")
	privateErr := errors.New("private socket not found")
	unit := xraystatus.SystemdUnit{
		DialSystem:  dialTo(nil, systemErr),
		DialPrivate: dialTo(nil, privateErr),
	}

	_, err := unit.QueryUnit(context.Background(), "xray.service")
	if err == nil {
		t.Fatal("query unit succeeded with both dials failing")
	}
	// Only the fallback's error used to be returned, so a dbus-less
	// unprivileged host logged the misleading private-socket EPERM (#17).
	if !errors.Is(err, systemErr) || !errors.Is(err, privateErr) {
		t.Errorf("error %v unwraps to only one cause; want both", err)
	}
	for _, cause := range []string{"system bus EPERM", "private socket not found"} {
		if !strings.Contains(err.Error(), cause) {
			t.Errorf("error %q does not name %q", err, cause)
		}
	}
}

func TestPropertyReadFailureDoesNotTriggerFallback(t *testing.T) {
	// Dial-error-only semantics: once a dial succeeded, a failing property
	// read is a unit/query problem, not a connection problem — retrying on
	// the other socket would fail identically.
	system := &fakeConn{propsErr: errors.New("unit xray.service not found")}
	unit := xraystatus.SystemdUnit{
		DialSystem: dialTo(system, nil),
		DialPrivate: func(context.Context) (xraystatus.UnitConn, error) {
			t.Fatal("private socket dialed after a successful system-bus dial")
			return nil, nil
		},
	}

	_, err := unit.QueryUnit(context.Background(), "xray.service")
	if err == nil || !strings.Contains(err.Error(), "unit xray.service not found") {
		t.Fatalf("error = %v, want the property-read failure surfaced", err)
	}
	if !system.closed {
		t.Error("connection not closed after the failed query")
	}
}
