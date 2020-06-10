// -*- Mode: Go; indent-tabs-mode: t -*-

/*
 * Copyright (C) 2020 Canonical Ltd
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU General Public License version 3 as
 * published by the Free Software Foundation.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * along with this program.  If not, see <http://www.gnu.org/licenses/>.
 *
 */

package cgroup_test

import (
	"fmt"
	"io/ioutil"
	"os"

	"github.com/godbus/dbus"
	. "gopkg.in/check.v1"

	"github.com/snapcore/snapd/dbusutil"
	"github.com/snapcore/snapd/dbusutil/dbustest"
	"github.com/snapcore/snapd/dirs"
	"github.com/snapcore/snapd/features"
	"github.com/snapcore/snapd/logger"
	"github.com/snapcore/snapd/sandbox/cgroup"
	"github.com/snapcore/snapd/testutil"
)

func enableFeatures(c *C, ff ...features.SnapdFeature) {
	c.Assert(os.MkdirAll(dirs.FeaturesDir, 0755), IsNil)
	for _, f := range ff {
		c.Assert(ioutil.WriteFile(f.ControlFile(), nil, 0755), IsNil)
	}
}

type trackingSuite struct{}

var _ = Suite(&trackingSuite{})

func (s *trackingSuite) SetUpTest(c *C) {
	dirs.SetRootDir(c.MkDir())
}

func (s *trackingSuite) TearDownTest(c *C) {
	dirs.SetRootDir("")
}

// CreateTransientScopeForTracking is a no-op when refresh app awareness is off
func (s *trackingSuite) TestCreateTransientScopeForTrackingFeatureDisabled(c *C) {
	noDBus := func() (*dbus.Conn, error) {
		return nil, fmt.Errorf("dbus should not have been used")
	}
	restore := dbusutil.MockConnections(noDBus, noDBus)
	defer restore()

	c.Assert(features.RefreshAppAwareness.IsEnabled(), Equals, false)
	err := cgroup.CreateTransientScopeForTracking("snap.pkg.app")
	c.Check(err, IsNil)
}

// CreateTransientScopeForTracking does stuff when refresh app awareness is on
func (s *trackingSuite) TestCreateTransientScopeForTrackingFeatureEnabled(c *C) {
	// Pretend that refresh app awareness is enabled
	enableFeatures(c, features.RefreshAppAwareness)
	c.Assert(features.RefreshAppAwareness.IsEnabled(), Equals, true)
	// Pretend we are a non-root user so that session bus is used.
	restore := cgroup.MockOsGetuid(12345)
	defer restore()
	// Pretend our PID is this value.
	restore = cgroup.MockOsGetpid(312123)
	defer restore()
	// Rig the random UUID generator to return this value.
	uuid := "cc98cd01-6a25-46bd-b71b-82069b71b770"
	restore = cgroup.MockRandomUUID(uuid)
	defer restore()
	// Replace interactions with DBus so that only session bus is available and responds with our logic.
	conn, err := dbustest.Connection(func(msg *dbus.Message, n int) ([]*dbus.Message, error) {
		switch n {
		case 0:
			return []*dbus.Message{happyResponseToStartTransientUnit(c, msg, "snap.pkg.app."+uuid+".scope", 312123)}, nil
		}
		return nil, fmt.Errorf("unexpected message #%d: %s", n, msg)
	})
	c.Assert(err, IsNil)
	restore = dbusutil.MockOnlySessionBusAvailable(conn)
	defer restore()
	// Replace the cgroup analyzer function
	restore = cgroup.MockCgroupProcessPathInTrackingCgroup(func(pid int) (string, error) {
		return "/user.slice/user-12345.slice/user@12345.service/snap.pkg.app." + uuid + ".scope", nil
	})
	defer restore()

	err = cgroup.CreateTransientScopeForTracking("snap.pkg.app")
	c.Check(err, IsNil)
}

func (s *trackingSuite) TestCreateTransientScopeForTrackingUnhappyNotRootGeneric(c *C) {
	// Pretend that refresh app awareness is enabled
	enableFeatures(c, features.RefreshAppAwareness)

	// Hand out stub connections to both the system and session bus.
	// Neither is really used here but they must appear to be available.
	restore := dbusutil.MockConnections(dbustest.StubConnection, dbustest.StubConnection)
	defer restore()

	// Pretend we are a non-root user so that session bus is used.
	restore = cgroup.MockOsGetuid(12345)
	defer restore()
	// Pretend our PID is this value.
	restore = cgroup.MockOsGetpid(312123)
	defer restore()

	// Disable the cgroup analyzer function as we don't expect it to be used in this test.
	restore = cgroup.MockCgroupProcessPathInTrackingCgroup(func(pid int) (string, error) {
		panic("we are not expecting this call")
	})
	defer restore()

	// Pretend that attempting to create a transient scope fails with a canned error.
	restore = cgroup.MockDoCreateTransientScope(func(conn *dbus.Conn, unitName string, pid int) error {
		return fmt.Errorf("cannot create transient scope for testing")
	})
	defer restore()

	// Create a transient scope and see it fail according to how doCreateTransientScope is rigged.
	err := cgroup.CreateTransientScopeForTracking("snap.pkg.app")
	c.Assert(err, ErrorMatches, "cannot create transient scope for testing")

	// Calling StartTransientUnit fails with org.freedesktop.DBus.UnknownMethod error.
	// This is possible on old systemd or on deputy systemd.
	restore = cgroup.MockDoCreateTransientScope(func(conn *dbus.Conn, unitName string, pid int) error {
		return cgroup.ErrDBusUnknownMethod
	})
	defer restore()

	// Attempts to create a transient scope fail with a special error
	// indicating that we cannot track application process.
	err = cgroup.CreateTransientScopeForTracking("snap.pkg.app")
	c.Assert(err, ErrorMatches, "cannot track application process")

	// Calling StartTransientUnit fails with org.freedesktop.DBus.Spawn.ChildExited error.
	// This is possible where we try to activate socket activate session bus
	// but it's not available OR when we try to socket activate systemd --user.
	restore = cgroup.MockDoCreateTransientScope(func(conn *dbus.Conn, unitName string, pid int) error {
		return cgroup.ErrDBusSpawnChildExited
	})
	defer restore()

	// Attempts to create a transient scope fail with a special error
	// indicating that we cannot track application process and because we are
	// not root, we do not attempt to fall back to the system bus.
	err = cgroup.CreateTransientScopeForTracking("snap.pkg.app")
	c.Assert(err, ErrorMatches, "cannot track application process")
}

func (s *trackingSuite) TestCreateTransientScopeForTrackingUnhappyRootFallback(c *C) {
	// Pretend that refresh app awareness is enabled
	enableFeatures(c, features.RefreshAppAwareness)

	// Hand out stub connections to both the system and session bus.
	// Neither is really used here but they must appear to be available.
	restore := dbusutil.MockConnections(dbustest.StubConnection, dbustest.StubConnection)
	defer restore()

	// Pretend we are a root user so that we attempt to use the system bus as fallback.
	restore = cgroup.MockOsGetuid(0)
	defer restore()
	// Pretend our PID is this value.
	restore = cgroup.MockOsGetpid(312123)
	defer restore()

	// Rig the random UUID generator to return this value.
	uuid := "cc98cd01-6a25-46bd-b71b-82069b71b770"
	restore = cgroup.MockRandomUUID(uuid)
	defer restore()

	// Calling StartTransientUnit fails on the session and then works on the system bus.
	// This test emulates a root user falling back from the session bus to the system bus.
	n := 0
	restore = cgroup.MockDoCreateTransientScope(func(conn *dbus.Conn, unitName string, pid int) error {
		n++
		switch n {
		case 1:
			// On first try we fail. This is when we used the session bus/
			return cgroup.ErrDBusSpawnChildExited
		case 2:
			// On second try we succeed.
			return nil
		}
		panic("expected to call doCreateTransientScope at most twice")
	})
	defer restore()

	// Rig the cgroup analyzer to pretend that we got placed into the system slice.
	restore = cgroup.MockCgroupProcessPathInTrackingCgroup(func(pid int) (string, error) {
		c.Assert(pid, Equals, 312123)
		return "/system.slice/snap.pkg.app." + uuid + ".scope", nil
	})
	defer restore()

	// Attempts to create a transient scope fail with a special error
	// indicating that we cannot track application process and but because we were
	// root we attempted to fall back to the system bus.
	err := cgroup.CreateTransientScopeForTracking("snap.pkg.app")
	c.Assert(err, IsNil)
}

func (s *trackingSuite) TestCreateTransientScopeForTrackingUnhappyRootFailedFallback(c *C) {
	// Pretend that refresh app awareness is enabled
	enableFeatures(c, features.RefreshAppAwareness)

	// Make it appear that session bus is there but system bus is not.
	noSystemBus := func() (*dbus.Conn, error) {
		return nil, fmt.Errorf("system bus is not available for testing")
	}
	restore := dbusutil.MockConnections(noSystemBus, dbustest.StubConnection)
	defer restore()

	// Pretend we are a root user so that we attempt to use the system bus as fallback.
	restore = cgroup.MockOsGetuid(0)
	defer restore()
	// Pretend our PID is this value.
	restore = cgroup.MockOsGetpid(312123)
	defer restore()

	// Rig the random UUID generator to return this value.
	uuid := "cc98cd01-6a25-46bd-b71b-82069b71b770"
	restore = cgroup.MockRandomUUID(uuid)
	defer restore()

	// Calling StartTransientUnit fails so that we try to use the system bus as fallback.
	restore = cgroup.MockDoCreateTransientScope(func(conn *dbus.Conn, unitName string, pid int) error {
		return cgroup.ErrDBusSpawnChildExited
	})
	defer restore()

	// Disable the cgroup analyzer function as we don't expect it to be used in this test.
	restore = cgroup.MockCgroupProcessPathInTrackingCgroup(func(pid int) (string, error) {
		panic("we are not expecting this call")
	})
	defer restore()

	// Attempts to create a transient scope fail with a special error
	// indicating that we cannot track application process.
	err := cgroup.CreateTransientScopeForTracking("snap.pkg.app")
	c.Assert(err, ErrorMatches, "cannot track application process")
}

func (s *trackingSuite) TestCreateTransientScopeForTrackingUnhappyNoDBus(c *C) {
	// Pretend that refresh app awareness is enabled
	enableFeatures(c, features.RefreshAppAwareness)

	// Make it appear that DBus is entirely unavailable.
	noBus := func() (*dbus.Conn, error) {
		return nil, fmt.Errorf("dbus is not available for testing")
	}
	restore := dbusutil.MockConnections(noBus, noBus)
	defer restore()

	// Pretend we are a root user so that we attempt to use the system bus as fallback.
	restore = cgroup.MockOsGetuid(0)
	defer restore()
	// Pretend our PID is this value.
	restore = cgroup.MockOsGetpid(312123)
	defer restore()

	// Rig the random UUID generator to return this value.
	uuid := "cc98cd01-6a25-46bd-b71b-82069b71b770"
	restore = cgroup.MockRandomUUID(uuid)
	defer restore()

	// Calling StartTransientUnit is not attempted without a DBus connection.
	restore = cgroup.MockDoCreateTransientScope(func(conn *dbus.Conn, unitName string, pid int) error {
		panic("we are not expecting this call")
	})
	defer restore()

	// Disable the cgroup analyzer function as we don't expect it to be used in this test.
	restore = cgroup.MockCgroupProcessPathInTrackingCgroup(func(pid int) (string, error) {
		panic("we are not expecting this call")
	})
	defer restore()

	// Attempts to create a transient scope fail with a special error
	// indicating that we cannot track application process.
	err := cgroup.CreateTransientScopeForTracking("snap.pkg.app")
	c.Assert(err, ErrorMatches, "cannot track application process")
}

func (s *trackingSuite) TestCreateTransientScopeForTrackingSilentlyFails(c *C) {
	// Pretend that refresh app awareness is enabled
	enableFeatures(c, features.RefreshAppAwareness)

	// Hand out stub connections to both the system and session bus.
	// Neither is really used here but they must appear to be available.
	restore := dbusutil.MockConnections(dbustest.StubConnection, dbustest.StubConnection)
	defer restore()

	// Pretend we are a non-root user.
	restore = cgroup.MockOsGetuid(12345)
	defer restore()
	// Pretend our PID is this value.
	restore = cgroup.MockOsGetpid(312123)
	defer restore()

	// Rig the random UUID generator to return this value.
	uuid := "cc98cd01-6a25-46bd-b71b-82069b71b770"
	restore = cgroup.MockRandomUUID(uuid)
	defer restore()

	// Calling StartTransientUnit succeeds but in reality does not move our
	// process to the new cgroup hierarchy. This can happen when systemd
	// version is < 238 and when the calling user is in a hierarchy that is
	// owned by another user. One example is a user logging in remotely over
	// ssh.
	restore = cgroup.MockDoCreateTransientScope(func(conn *dbus.Conn, unitName string, pid int) error {
		return nil
	})
	defer restore()

	// Rig the cgroup analyzer to pretend that we are not placed in a snap-related slice.
	restore = cgroup.MockCgroupProcessPathInTrackingCgroup(func(pid int) (string, error) {
		c.Assert(pid, Equals, 312123)
		return "/system.slice/foo.service", nil
	})
	defer restore()

	// Attempts to create a transient scope fail with a special error
	// indicating that we cannot track application process even though
	// the DBus call has returned no error.
	err := cgroup.CreateTransientScopeForTracking("snap.pkg.app")
	c.Assert(err, ErrorMatches, "cannot track application process")
}

func happyResponseToStartTransientUnit(c *C, msg *dbus.Message, scopeName string, pid int) *dbus.Message {
	// XXX: Those types might live in a package somewhere
	type Property struct {
		Name  string
		Value interface{}
	}
	type Unit struct {
		Name  string
		Props []Property
	}
	// Signature of StartTransientUnit, string, string, array of Property and array of Unit (see above).
	requestSig := dbus.SignatureOf("", "", []Property{}, []Unit{})

	c.Assert(msg.Type, Equals, dbus.TypeMethodCall)
	c.Check(msg.Flags, Equals, dbus.Flags(0))
	c.Check(msg.Headers, DeepEquals, map[dbus.HeaderField]dbus.Variant{
		dbus.FieldDestination: dbus.MakeVariant("org.freedesktop.systemd1"),
		dbus.FieldPath:        dbus.MakeVariant(dbus.ObjectPath("/org/freedesktop/systemd1")),
		dbus.FieldInterface:   dbus.MakeVariant("org.freedesktop.systemd1.Manager"),
		dbus.FieldMember:      dbus.MakeVariant("StartTransientUnit"),
		dbus.FieldSignature:   dbus.MakeVariant(requestSig),
	})
	c.Check(msg.Body, DeepEquals, []interface{}{
		scopeName,
		"fail",
		[][]interface{}{
			{"PIDs", dbus.MakeVariant([]uint32{uint32(pid)})},
		},
		[][]interface{}{},
	})

	responseSig := dbus.SignatureOf(dbus.ObjectPath(""))
	return &dbus.Message{
		Type: dbus.TypeMethodReply,
		Headers: map[dbus.HeaderField]dbus.Variant{
			dbus.FieldReplySerial: dbus.MakeVariant(msg.Serial()),
			dbus.FieldSender:      dbus.MakeVariant(":1"), // This does not matter.
			// dbus.FieldDestination is provided automatically by DBus test helper.
			dbus.FieldSignature: dbus.MakeVariant(responseSig),
		},
		// The object path returned in the body is not used by snap run yet.
		Body: []interface{}{dbus.ObjectPath("/org/freedesktop/systemd1/job/1462")},
	}
}

func unhappyResponseToStartTransientUnit(c *C, msg *dbus.Message, errMsg string) *dbus.Message {
	c.Assert(msg.Type, Equals, dbus.TypeMethodCall)
	// ignore the message and just produce an error response
	return &dbus.Message{
		Type: dbus.TypeError,
		Headers: map[dbus.HeaderField]dbus.Variant{
			dbus.FieldReplySerial: dbus.MakeVariant(msg.Serial()),
			dbus.FieldSender:      dbus.MakeVariant(":1"), // This does not matter.
			// dbus.FieldDestination is provided automatically by DBus test helper.
			dbus.FieldErrorName: dbus.MakeVariant(errMsg),
		},
	}
}

func (s *trackingSuite) TestDoCreateTransientScopeHappy(c *C) {
	conn, err := dbustest.Connection(func(msg *dbus.Message, n int) ([]*dbus.Message, error) {
		switch n {
		case 0:
			return []*dbus.Message{happyResponseToStartTransientUnit(c, msg, "foo.scope", 312123)}, nil
		}
		return nil, fmt.Errorf("unexpected message #%d: %s", n, msg)
	})

	c.Assert(err, IsNil)
	defer conn.Close()
	err = cgroup.DoCreateTransientScope(conn, "foo.scope", 312123)
	c.Assert(err, IsNil)
}

func (s *trackingSuite) TestDoCreateTransientScopeForwardedErrors(c *C) {
	// Certain errors are forwareded and handled in the logic calling into
	// DoCreateTransientScope. Those are tested here.
	for _, errMsg := range []string{
		"org.freedesktop.DBus.Error.NameHasNoOwner",
		"org.freedesktop.DBus.Error.UnknownMethod",
		"org.freedesktop.DBus.Error.Spawn.ChildExited",
	} {
		conn, err := dbustest.Connection(func(msg *dbus.Message, n int) ([]*dbus.Message, error) {
			switch n {
			case 0:
				return []*dbus.Message{unhappyResponseToStartTransientUnit(c, msg, errMsg)}, nil
			}
			return nil, fmt.Errorf("unexpected message #%d: %s", n, msg)
		})
		c.Assert(err, IsNil)
		defer conn.Close()
		err = cgroup.DoCreateTransientScope(conn, "foo.scope", 312123)
		c.Assert(err, ErrorMatches, errMsg)
	}
}

func (s *trackingSuite) TestDoCreateTransientScopeClashingScopeName(c *C) {
	// In case our UUID algorithm is bad and systemd reports that an unit with
	// identical name already exists, we provide a special error handler for that.
	errMsg := "org.freedesktop.systemd1.UnitExists"
	conn, err := dbustest.Connection(func(msg *dbus.Message, n int) ([]*dbus.Message, error) {
		switch n {
		case 0:
			return []*dbus.Message{unhappyResponseToStartTransientUnit(c, msg, errMsg)}, nil
		}
		return nil, fmt.Errorf("unexpected message #%d: %s", n, msg)
	})
	c.Assert(err, IsNil)
	defer conn.Close()
	err = cgroup.DoCreateTransientScope(conn, "foo.scope", 312123)
	c.Assert(err, ErrorMatches, "cannot create transient scope: scope .* clashed: .*")
}

func (s *trackingSuite) TestDoCreateTransientScopeOtherDBusErrors(c *C) {
	// Other DBus errors are not special-cased and cause a generic failure handler.
	errMsg := "org.example.BadHairDay"
	conn, err := dbustest.Connection(func(msg *dbus.Message, n int) ([]*dbus.Message, error) {
		switch n {
		case 0:
			return []*dbus.Message{unhappyResponseToStartTransientUnit(c, msg, errMsg)}, nil
		}
		return nil, fmt.Errorf("unexpected message #%d: %s", n, msg)
	})
	c.Assert(err, IsNil)
	defer conn.Close()
	err = cgroup.DoCreateTransientScope(conn, "foo.scope", 312123)
	c.Assert(err, ErrorMatches, `cannot create transient scope: DBus error "org.example.BadHairDay": \[\]`)
}

func (s *trackingSuite) TestSessionOrMaybeSystemBusTotalFailureForRoot(c *C) {
	system := func() (*dbus.Conn, error) {
		return nil, fmt.Errorf("system bus unavailable for testing")
	}
	session := func() (*dbus.Conn, error) {
		return nil, fmt.Errorf("session bus unavailable for testing")
	}
	restore := dbusutil.MockConnections(system, session)
	defer restore()
	logBuf, restore := logger.MockLogger()
	defer restore()
	os.Setenv("SNAPD_DEBUG", "true")
	defer os.Unsetenv("SNAPD_DEBUG")

	uid := 0
	isSession, conn, err := cgroup.SessionOrMaybeSystemBus(uid)
	c.Assert(err, ErrorMatches, "system bus unavailable for testing")
	c.Check(conn, IsNil)
	c.Check(isSession, Equals, false)
	c.Check(logBuf.String(), testutil.Contains, "DEBUG: session bus is not available: session bus unavailable for testing\n")
	c.Check(logBuf.String(), testutil.Contains, "DEBUG: falling back to system bus\n")
	c.Check(logBuf.String(), testutil.Contains, "DEBUG: system bus is not available: system bus unavailable for testing\n")
}

func (s *trackingSuite) TestSessionOrMaybeSystemBusFallbackForRoot(c *C) {
	system := func() (*dbus.Conn, error) {
		return dbustest.StubConnection()
	}
	session := func() (*dbus.Conn, error) {
		return nil, fmt.Errorf("session bus unavailable for testing")
	}
	restore := dbusutil.MockConnections(system, session)
	defer restore()
	logBuf, restore := logger.MockLogger()
	defer restore()
	os.Setenv("SNAPD_DEBUG", "true")
	defer os.Unsetenv("SNAPD_DEBUG")

	uid := 0
	isSession, conn, err := cgroup.SessionOrMaybeSystemBus(uid)
	c.Assert(err, IsNil)
	conn.Close()
	c.Check(isSession, Equals, false)
	c.Check(logBuf.String(), testutil.Contains, "DEBUG: session bus is not available: session bus unavailable for testing\n")
	c.Check(logBuf.String(), testutil.Contains, "DEBUG: falling back to system bus\n")
	c.Check(logBuf.String(), testutil.Contains, "DEBUG: using system bus now, session bus was not available\n")
}

func (s *trackingSuite) TestSessionOrMaybeSystemBusNonRootSessionFailure(c *C) {
	system := func() (*dbus.Conn, error) {
		return dbustest.StubConnection()
	}
	session := func() (*dbus.Conn, error) {
		return nil, fmt.Errorf("session bus unavailable for testing")
	}
	restore := dbusutil.MockConnections(system, session)
	defer restore()
	logBuf, restore := logger.MockLogger()
	defer restore()
	os.Setenv("SNAPD_DEBUG", "true")
	defer os.Unsetenv("SNAPD_DEBUG")

	uid := 12345
	isSession, conn, err := cgroup.SessionOrMaybeSystemBus(uid)
	c.Assert(err, ErrorMatches, "session bus unavailable for testing")
	c.Check(conn, IsNil)
	c.Check(isSession, Equals, false)
	c.Check(logBuf.String(), testutil.Contains, "DEBUG: session bus is not available: session bus unavailable for testing\n")
}

func (s *trackingSuite) TestConfirmSystemdServiceTrackingHappy(c *C) {
	// Pretend our PID is this value.
	restore := cgroup.MockOsGetpid(312123)
	defer restore()
	// Replace the cgroup analyzer function
	restore = cgroup.MockCgroupProcessPathInTrackingCgroup(func(pid int) (string, error) {
		c.Assert(pid, Equals, 312123)
		return "/user.slice/user-12345.slice/user@12345.service/snap.pkg.app.service", nil
	})
	defer restore()

	// With the cgroup path faked as above, we are being tracked as the systemd
	// service so no error is reported.
	err := cgroup.ConfirmSystemdServiceTracking("snap.pkg.app")
	c.Assert(err, IsNil)
}

func (s *trackingSuite) TestConfirmSystemdServiceTrackingSad(c *C) {
	// Pretend our PID is this value.
	restore := cgroup.MockOsGetpid(312123)
	defer restore()
	// Replace the cgroup analyzer function
	restore = cgroup.MockCgroupProcessPathInTrackingCgroup(func(pid int) (string, error) {
		c.Assert(pid, Equals, 312123)
		// Tracking path of a gnome terminal helper process. Meant to illustrate a tracking but not related to a snap application.
		return "user.slice/user-12345.slice/user@12345.service/apps.slice/apps-org.gnome.Terminal.slice/vte-spawn-e640104a-cddf-4bd8-ba4b-2c1baf0270c3.scope", nil
	})
	defer restore()

	// With the cgroup path faked as above, tracking is not effective.
	err := cgroup.ConfirmSystemdServiceTracking("snap.pkg.app")
	c.Assert(err, Equals, cgroup.ErrCannotTrackProcess)
}
